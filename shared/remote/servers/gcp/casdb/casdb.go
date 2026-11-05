// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package casdb

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/bigtable"
	"cloud.google.com/go/spanner"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
)

var ErrNotFound = errors.New("not found")
var ErrAlreadyExists = errors.New("already exists")
var ErrInvalidContent = errors.New("invalid content descriptor")

const (
	maxRowSetBatch             = 100
	maxLocalHeadersConcurrency = 40
	defaultHeadersSemSize      = 2500
	headersSemPoolMultiplier   = 50
)

type CASDB struct {
	dbClient     *spanner.Client
	btClient     *bigtable.Client
	mapsTable    *bigtable.Table
	objectsTable *bigtable.Table
	headersSem   chan struct{}
}

func New(ctx context.Context, spannerDB, projectID, btInstance, btAppProfile string, btConnPoolSize int) (*CASDB, error) {
	sessionPoolConfig := spanner.SessionPoolConfig{
		MinOpened:     400,
		MaxOpened:     5000,
		WriteSessions: 0.2,
	}

	numChannels := option.WithGRPCConnectionPool(int(sessionPoolConfig.MaxOpened) / 100)
	client, err := spanner.NewClientWithConfig(ctx, spannerDB, spanner.ClientConfig{
		SessionPoolConfig: sessionPoolConfig,
	}, numChannels)
	if err != nil {
		return nil, err
	}

	btClient, err := bigtable.NewClientWithConfig(ctx, projectID, btInstance, bigtable.ClientConfig{
		AppProfile: btAppProfile,
	}, bigtableClientOptions(btConnPoolSize)...)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to create bigtable client: %w", err)
	}
	mapsTable := btClient.Open("maps")
	objectsTable := btClient.Open("objects")

	semSize := defaultHeadersSemSize
	if btConnPoolSize > 0 {
		semSize = btConnPoolSize * headersSemPoolMultiplier
	}

	cas := &CASDB{
		dbClient:     client,
		btClient:     btClient,
		mapsTable:    mapsTable,
		objectsTable: objectsTable,
		headersSem:   make(chan struct{}, semSize),
	}
	return cas, nil
}

// bigtableClientOptions sets the Bigtable client's gRPC connection pool size.
// A non-positive value keeps the library default (4). No-op if the
// CBT_BIGTABLE_CONN_POOL env var is set (that path ignores WithGRPCConnectionPool).
func bigtableClientOptions(btConnPoolSize int) []option.ClientOption {
	if btConnPoolSize <= 0 {
		return nil
	}
	return []option.ClientOption{option.WithGRPCConnectionPool(btConnPoolSize)}
}

func (c *CASDB) BTClient() *bigtable.Client {
	return c.btClient
}

func (c *CASDB) SpannerClient() *spanner.Client {
	return c.dbClient
}

func (c *CASDB) Close() error {
	c.dbClient.Close()
	return c.btClient.Close()
}

func int64ToBytes(val int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(val))
	return b
}

func bytesToInt64(b []byte) int64 {
	if len(b) < 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(b))
}

// internObjectType returns a compile-time constant string for known ABFS object
// types so cached ObjectHeaders share static strings without heap allocations.
// Note: Any new object type not listed in this switch will still work via the
// default branch, but will not benefit from interning and will incur a heap allocation.
func internObjectType(b []byte) string {
	switch string(b) {
	case string(entities.ObjectTypeBlob):
		return string(entities.ObjectTypeBlob)
	case string(gitobjects.ObjectTypeDir):
		return string(gitobjects.ObjectTypeDir)
	case string(gitobjects.ObjectTypeCommit):
		return string(gitobjects.ObjectTypeCommit)
	case string(gitobjects.ObjectTypeTag):
		return string(gitobjects.ObjectTypeTag)
	case string(gitobjects.ObjectTypeSymRef):
		return string(gitobjects.ObjectTypeSymRef)
	case string(gitobjects.ObjectTypeDeepTree):
		return string(gitobjects.ObjectTypeDeepTree)
	case string(objects.ObjectTypeDeepTreeRequest):
		return string(objects.ObjectTypeDeepTreeRequest)
	default:
		// Unlisted types fall back to a heap allocation.
		return string(b)
	}
}

func (c *CASDB) AddObject(ctx context.Context, hdr *ObjectHeader, content *ObjectContent) error {
	now := bigtable.Now()
	mut := bigtable.NewMutation()
	mut.Set("meta", "type", now, []byte(hdr.Type))
	mut.Set("meta", "size", now, int64ToBytes(hdr.Size))
	mut.Set("data", "encoding", now, []byte(content.Encoding))
	mut.Set("data", "content", now, []byte(content.Content))

	if err := c.objectsTable.Apply(ctx, hdr.Id.String(), mut); err != nil {
		return fmt.Errorf("failed to add object %s to Bigtable: %w", hdr.Id, err)
	}

	return nil
}

// GetObjectData fetches metadata and content descriptors in a single Bigtable point read.
func (c *CASDB) GetObjectData(ctx context.Context, hash entities.Hash) (*ObjectHeader, *ObjectContent, error) {
	row, err := c.objectsTable.ReadRow(ctx, hash.String(), bigtable.RowFilter(bigtable.LatestNFilter(1)))
	if err != nil {
		return nil, nil, err
	}

	if row != nil && len(row["meta"]) > 0 && len(row["data"]) > 0 {
		hdr := &ObjectHeader{Id: hash}
		for _, item := range row["meta"] {
			switch item.Column {
			case "meta:type":
				hdr.Type = internObjectType(item.Value)
			case "meta:size":
				hdr.Size = bytesToInt64(item.Value)
			}
		}
		content := &ObjectContent{}
		for _, item := range row["data"] {
			switch item.Column {
			case "data:encoding":
				content.Encoding = string(item.Value)
			case "data:content":
				content.Content = string(item.Value)
			}
		}
		return hdr, content, nil
	}

	return nil, nil, ErrNotFound
}

func (c *CASDB) GetHeader(ctx context.Context, hash entities.Hash) (*ObjectHeader, error) {
	row, err := c.objectsTable.ReadRow(ctx, hash.String(), bigtable.RowFilter(bigtable.ChainFilters(bigtable.FamilyFilter("meta"), bigtable.LatestNFilter(1))))
	if err != nil {
		return nil, err
	}
	if row == nil || len(row["meta"]) == 0 {
		return nil, ErrNotFound
	}
	obj := &ObjectHeader{Id: hash}
	for _, item := range row["meta"] {
		switch item.Column {
		case "meta:type":
			obj.Type = internObjectType(item.Value)
		case "meta:size":
			obj.Size = bytesToInt64(item.Value)
		}
	}
	return obj, nil
}

func (c *CASDB) GetHeaders(ctx context.Context, hashes []entities.Hash) ([]*ObjectHeader, error) {
	filter := bigtable.ChainFilters(bigtable.FamilyFilter("meta"), bigtable.LatestNFilter(1))
	var outMu sync.Mutex

	rowKeys := make(bigtable.RowList, len(hashes))
	for i, h := range hashes {
		rowKeys[i] = h.String()
	}
	slices.Sort(rowKeys)
	rowKeys = slices.Compact(rowKeys)

	out := make([]*ObjectHeader, 0, len(rowKeys))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxLocalHeadersConcurrency)

	for rowList := range slices.Chunk(rowKeys, maxRowSetBatch) {
		g.Go(func() error {
			if c.headersSem != nil {
				select {
				case c.headersSem <- struct{}{}:
					defer func() { <-c.headersSem }()
				case <-gctx.Done():
					return gctx.Err()
				}
			}

			batch := make([]*ObjectHeader, 0, len(rowList))
			err := c.objectsTable.ReadRows(gctx, rowList, func(row bigtable.Row) bool {
				obj := &ObjectHeader{Id: entities.NewHash(row.Key())}
				for _, item := range row["meta"] {
					switch item.Column {
					case "meta:type":
						obj.Type = internObjectType(item.Value)
					case "meta:size":
						obj.Size = bytesToInt64(item.Value)
					}
				}
				if len(row["meta"]) > 0 {
					batch = append(batch, obj)
				}
				return true
			}, bigtable.RowFilter(filter))
			if err != nil {
				return err
			}

			if len(batch) > 0 {
				outMu.Lock()
				out = append(out, batch...)
				outMu.Unlock()
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return out, nil
}

func (c *CASDB) GetContent(ctx context.Context, hash entities.Hash) (*ObjectContent, error) {
	row, err := c.objectsTable.ReadRow(ctx, hash.String(), bigtable.RowFilter(bigtable.ChainFilters(bigtable.FamilyFilter("data"), bigtable.LatestNFilter(1))))
	if err != nil {
		return nil, err
	}
	if row == nil || len(row["data"]) == 0 {
		return nil, ErrNotFound
	}
	obj := &ObjectContent{}
	for _, item := range row["data"] {
		switch item.Column {
		case "data:encoding":
			obj.Encoding = string(item.Value)
		case "data:content":
			obj.Content = string(item.Value)
		}
	}
	return obj, nil
}

func (c *CASDB) AddMapResult(ctx context.Context, from, to entities.Hash) error {
	// Maps are keyed by "from#to" and used Naked column.
	// The value is don't care and only signals existence.
	mut := bigtable.NewMutation()
	mut.Set("cf", "", bigtable.Now(), []byte("1"))
	rowKey := fmt.Sprintf("%s#%s", from, to)
	return c.mapsTable.Apply(ctx, rowKey, mut)
}

// MapResults returns the results added for from, oldest first, as file-based
// does. Rows are keyed by result, so the order comes from the cell timestamps;
// results added in the same millisecond keep the row key order. Each server
// sets the timestamps from its own clock, so with several servers the order of
// results added close together follows their clock skew.
func (c *CASDB) MapResults(ctx context.Context, from entities.Hash) ([]entities.Hash, error) {
	prefix := from.String() + "#"
	type mapResult struct {
		hash  entities.Hash
		added bigtable.Timestamp
	}
	var results []mapResult

	filter := bigtable.ChainFilters(bigtable.CellsPerRowLimitFilter(1), bigtable.StripValueFilter())
	err := c.mapsTable.ReadRows(ctx, bigtable.PrefixRange(prefix), func(row bigtable.Row) bool {
		toStr := strings.TrimPrefix(row.Key(), prefix)
		if toStr != "" && len(row["cf"]) > 0 {
			results = append(results, mapResult{hash: entities.NewHash(toStr), added: row["cf"][0].Timestamp})
		}
		return true
	}, bigtable.RowFilter(filter))
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(results, func(a, b mapResult) int {
		return cmp.Compare(a.added, b.added)
	})
	hashes := make([]entities.Hash, len(results))
	for i, r := range results {
		hashes[i] = r.hash
	}
	return hashes, nil
}

func (c *CASDB) GetOrCreateProject(ctx context.Context, host, project string) (int64, error) {
	if id, err := c.GetProjectID(ctx, host, project); err != ErrNotFound {
		return id, err
	}
	if id, err := c.AddProject(ctx, host, project); err != ErrAlreadyExists {
		return id, err
	}
	return c.GetProjectID(ctx, host, project)
}

func (c *CASDB) GetProjectID(ctx context.Context, host, project string) (int64, error) {
	stmt := spanner.Statement{
		SQL: "SELECT ProjectId FROM Projects WHERE Host=@host AND Project=@project",
		Params: map[string]any{
			"host":    host,
			"project": project,
		},
	}
	var projectID int64 = -1
	err := c.dbClient.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		return row.Columns(&projectID)
	})
	if err != nil {
		return 0, err
	}
	if projectID == -1 {
		return 0, ErrNotFound
	}
	return projectID, nil
}

func (c *CASDB) GetProjectsByIDs(ctx context.Context, ids []int64) ([][2]string, error) {
	stmt := spanner.Statement{
		SQL: `SELECT
			  p.ProjectId,
			  p.Host,
			  p.Project
			FROM
			  UNNEST(@ids) AS i,
			  Projects AS p
			WHERE
			  p.ProjectId = i;`,
		Params: map[string]any{
			"ids": ids,
		},
	}

	idsMap := make(map[int64][2]string, len(ids))
	err := c.dbClient.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var projectID int64
		var server, project string
		if err := row.Columns(&projectID, &server, &project); err != nil {
			return err
		}
		idsMap[projectID] = [2]string{server, project}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([][2]string, 0, len(ids))
	for _, id := range ids {
		sp, ok := idsMap[id]
		if !ok {
			return nil, fmt.Errorf("project %d not found: %w", id, ErrNotFound)
		}
		result = append(result, sp)
	}
	return result, nil
}

func (c *CASDB) AddProject(ctx context.Context, host, project string) (int64, error) {
	stmt := spanner.Statement{
		SQL: "INSERT INTO Projects (Host, Project) VALUES (@host, @project) THEN RETURN ProjectId",
		Params: map[string]any{
			"host":    host,
			"project": project,
		},
	}
	var projectID int64
	_, err := c.dbClient.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		iter := txn.Query(ctx, stmt)
		defer iter.Stop()

		row, err := iter.Next()
		if err != nil {
			return err
		}
		if err := row.Columns(&projectID); err != nil {
			return err
		}

		return nil
	})
	if spanner.ErrCode(err) == codes.AlreadyExists {
		return 0, ErrAlreadyExists
	}
	return projectID, err
}

const maxRefsBatch = 2000

func (c *CASDB) AddRefs(ctx context.Context, projectID int64, refs []remote.RefHash) error {
	cols := []string{"ProjectId", "Ref", "UpdateTime", "Target"}

	m := make([]*spanner.Mutation, 0, min(maxRefsBatch, len(refs)))
	for refs := range slices.Chunk(refs, maxRefsBatch) {
		m = m[:0]

		for _, ref := range refs {
			t := spanner.NullString{StringVal: ref.Hash.String(), Valid: true}
			if ref.Hash.IsZero() {
				t = spanner.NullString{Valid: false}
			}

			m = append(m, spanner.InsertOrUpdate("Refs", cols,
				[]any{projectID, ref.Ref, time.Now(), t},
			))
		}
		_, err := c.dbClient.Apply(ctx, m)
		if err != nil {
			return fmt.Errorf("failed to add %d refs: %w", len(refs), err)
		}
	}
	return nil
}

func (c *CASDB) XchgRefs(ctx context.Context, projectID int64, refs []remote.RefXchg) error {
	if len(refs) == 0 {
		return nil
	}
	if len(refs) > maxRefsBatch {
		return fmt.Errorf("cannot xchg %d refs in a single transaction (max %d)", len(refs), maxRefsBatch)
	}

	cols := []string{"ProjectId", "Ref", "UpdateTime", "Target"}
	refNames := make([]string, len(refs))
	for i, ref := range refs {
		refNames[i] = ref.Ref
	}

	stmt := spanner.Statement{
		SQL: `SELECT
			  refs.Ref,
			  refs.Target
			FROM
			  UNNEST(@refs) AS r,
			  Refs AS refs
			WHERE
			  refs.ProjectId = @projectId AND refs.Ref = r;`,
		Params: map[string]any{
			"projectId": projectID,
			"refs":      refNames,
		},
	}

	_, err := c.dbClient.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		refsMap := make(map[string]entities.Hash, len(refs))
		err := txn.Query(ctx, stmt).Do(func(row *spanner.Row) error {
			var ref string
			var target spanner.NullString
			if err := row.Columns(&ref, &target); err != nil {
				return err
			}
			if target.Valid {
				refsMap[ref] = entities.NewHash(target.StringVal)
			}
			return nil
		})
		if err != nil {
			return err
		}

		for _, ref := range refs {
			actual := refsMap[ref.Ref]
			if actual != ref.Current {
				return &remote.ErrRefMismatch{
					Ref:     ref.Ref,
					Current: ref.Current,
					Actual:  actual,
				}
			}
			refsMap[ref.Ref] = ref.Desired
		}

		// Build mutations from the deduplicated refsMap rather than the raw refs
		// slice. This allows chained updates to the same ref within a single batch
		// (e.g., ref1: A -> B followed by ref1: B -> C) to emit a single mutation
		// with the final target (ref1 -> C), avoiding Spanner's restriction against
		// multiple mutations to the same row in one transaction.
		now := time.Now()
		m := make([]*spanner.Mutation, 0, len(refsMap))
		for ref, desired := range refsMap {
			t := spanner.NullString{StringVal: desired.String(), Valid: true}
			if desired.IsZero() {
				t = spanner.NullString{Valid: false}
			}
			m = append(m, spanner.InsertOrUpdate("Refs", cols,
				[]any{projectID, ref, now, t},
			))
		}
		return txn.BufferWrite(m)
	})
	if err != nil {
		return fmt.Errorf("failed to xchg %d refs: %w", len(refs), err)
	}
	return nil
}

func (c *CASDB) GetRefs(ctx context.Context, projectID int64, refs []string) ([]entities.Hash, error) {
	stmt := spanner.Statement{
		SQL: `SELECT
			  refs.Ref,
			  refs.Target
			FROM
			  UNNEST(@refs) AS r,
			  Refs AS refs
			WHERE
			  refs.ProjectId = @projectId AND refs.Ref = r;`,
		Params: map[string]any{
			"projectId": projectID,
			"refs":      refs,
		},
	}

	refsMap := make(map[string]entities.Hash, len(refs))
	err := c.dbClient.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var ref string
		var target spanner.NullString
		if err := row.Columns(&ref, &target); err != nil {
			return err
		}
		if target.Valid {
			refsMap[ref] = entities.NewHash(target.StringVal)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	result := make([]entities.Hash, len(refs))
	for i, r := range refs {
		result[i] = refsMap[r]
	}
	return result, nil
}

type ProjectRefHash struct {
	ProjectID int64
	Ref       string
	Hash      entities.Hash
}

func (c *CASDB) SearchRefs(ctx context.Context, projectID *int64, refPrefix string, limit int) ([]ProjectRefHash, error) {
	refPrefix = strings.Trim(refPrefix, "/")

	var stmt spanner.Statement
	if projectID == nil {
		stmt = spanner.Statement{
			SQL: "SELECT ProjectId, Ref, Target FROM Refs WHERE Ref = @refPrefix OR STARTS_WITH(Ref, @refPrefix || '/')",
			Params: map[string]any{
				"refPrefix": refPrefix,
			},
		}
	} else {
		stmt = spanner.Statement{
			SQL: "SELECT ProjectId, Ref, Target FROM Refs WHERE ProjectId = @projectId AND (Ref = @refPrefix OR STARTS_WITH(Ref, @refPrefix || '/'))",
			Params: map[string]any{
				"projectId": *projectID,
				"refPrefix": refPrefix,
			},
		}
	}
	if limit > 0 {
		stmt.SQL += " ORDER BY Ref DESC LIMIT @limit"
		stmt.Params["limit"] = limit
	}

	capHint := limit
	if capHint <= 0 {
		capHint = 16
	}
	return c.runProjectIDRefTargetQuery(ctx, stmt, capHint)
}

func hashesToStrings(hashes []entities.Hash) []string {
	hashesHex := make([]string, len(hashes))
	for i, h := range hashes {
		hashesHex[i] = h.String()
	}
	return hashesHex
}

func (c *CASDB) RefsTo(ctx context.Context, hashes []entities.Hash) ([]ProjectRefHash, error) {
	stmt := spanner.Statement{
		SQL: `SELECT
			  r.ProjectId,
			  r.Ref,
			  r.Target
			FROM
			  UNNEST(@hashes) AS h,
			  Refs AS r
			WHERE
			  r.Target = h;`,
		Params: map[string]any{
			"hashes": hashesToStrings(hashes),
		},
	}
	return c.runProjectIDRefTargetQuery(ctx, stmt, len(hashes))
}

func (c *CASDB) runProjectIDRefTargetQuery(ctx context.Context, stmt spanner.Statement, capHint int) ([]ProjectRefHash, error) {
	result := make([]ProjectRefHash, 0, capHint)
	err := c.dbClient.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		var projectID int64
		var ref string
		var target spanner.NullString
		if err := row.Columns(&projectID, &ref, &target); err != nil {
			return err
		}
		if target.Valid {
			result = append(result, ProjectRefHash{
				ProjectID: projectID,
				Ref:       ref,
				Hash:      entities.NewHash(target.StringVal),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}
