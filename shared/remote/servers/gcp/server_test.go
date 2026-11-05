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

package gcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"cloud.google.com/go/bigtable"
	"cloud.google.com/go/bigtable/bttest"
	"cloud.google.com/go/spanner"
	"cloud.google.com/go/spanner/apiv1/spannerpb"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers/gcp/casdb"
	"github.com/google/abfs/shared/remote/servers/gcp/depot"
)

type fakeURLDepot struct {
	depot.Depot
	urls map[string]string
}

func newFakeURLDepot(inner depot.Depot) *fakeURLDepot {
	return &fakeURLDepot{
		Depot: inner,
		urls:  make(map[string]string),
	}
}

func (f *fakeURLDepot) Name() string { return "fake-url" }

func (f *fakeURLDepot) Store(ctx context.Context, name string, r io.Reader) (depot.Chunk, error) {
	chunk, err := f.Depot.Store(ctx, name, r)
	chunk.Depot = "fake-url"
	f.urls[chunk.ID] = "https://fake.storage/blob/" + chunk.ID
	return chunk, err
}

func (f *fakeURLDepot) LinkedContent(ctx context.Context, s depot.Chunk) (remote.ContentLink, error) {
	url, ok := f.urls[s.ID]
	if !ok {
		return remote.ContentLink{}, errors.New("not found")
	}
	return remote.ContentLink{
		LinkType: remote.LinkTypeURL,
		Size:     uint64(s.Size),
		Content:  url,
	}, nil
}

func createTestServer(t *testing.T, master *depot.MasterDepot) *GCPServer {
	s := &GCPServer{
		headers:      make([]*lru.Cache[entities.Hash, CachedHeader], 256),
		content:      make([]*lru.Cache[entities.Hash, *ObjectContent], 256),
		depot:        master,
		storeLinks:   false,
		validateHash: true,
	}
	for i := range s.headers {
		var err error
		s.headers[i], err = lru.New[entities.Hash, CachedHeader](100)
		require.NoError(t, err)
		s.content[i], err = lru.New[entities.Hash, *ObjectContent](100)
		require.NoError(t, err)
	}
	return s
}

func TestGetObjectLinkedContentThresholdAndFallback(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("client-version", "0.1.17"))

	mem := depot.NewMemoryDepot("mem", 1000000)
	fake := newFakeURLDepot(mem)
	master := depot.NewMasterDepot([]depot.Depot{fake})

	s := createTestServer(t, master)
	s.linksContent = true
	s.SetLinksContentThreshold(100)

	// Store a chunk in the fake depot
	contentStr := "this is some data longer than 20 bytes for testing"
	chunks, err := master.StoreChunks(ctx, "test-obj-uuid", strings.NewReader(contentStr))
	require.NoError(t, err)
	chunksJSON, err := json.Marshal(chunks)
	require.NoError(t, err)

	hash := entities.Hash{1, 2, 3, 4}

	// 1. Small object (size <= threshold): should return regular stream, not links
	s.headers[hash[0]].Add(hash, newCachedHeader(&ObjectHeader{Id: hash, Size: 50, Type: "BLOB"}))
	s.content[hash[0]].Add(hash, &ObjectContent{Encoding: "raw", Content: string(chunksJSON)})

	r, err := s.GetObject(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, "raw", r.Encoding())
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, contentStr, string(data))
	r.Close()

	// 2. Large object (size > threshold): should return remote.EncodingLinks containing JSON LinksContent
	s.headers[hash[0]].Add(hash, newCachedHeader(&ObjectHeader{Id: hash, Size: 200, Type: "BLOB"}))

	r, err = s.GetObject(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, remote.EncodingLinks, r.Encoding())
	linksData, err := io.ReadAll(r)
	require.NoError(t, err)
	r.Close()

	var linksContent remote.LinksContent
	err = json.Unmarshal(linksData, &linksContent)
	require.NoError(t, err)
	assert.Equal(t, "raw", linksContent.Encoding)
	require.Len(t, linksContent.Links, 1)
	assert.Equal(t, remote.LinkTypeURL, linksContent.Links[0].LinkType)
	assert.True(t, strings.HasPrefix(linksContent.Links[0].Content, "https://fake.storage/blob/"))
}

type noLinksDepot struct {
	depot.Depot
}

func (n *noLinksDepot) LinkedContent(ctx context.Context, s depot.Chunk) (remote.ContentLink, error) {
	return remote.ContentLink{}, depot.ErrNoLinkedContent
}

func TestGetObjectLinkedContentFallbackWhenUnsupported(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("client-version", "0.1.17"))

	mem := depot.NewMemoryDepot("mem", 1000000)
	noLink := &noLinksDepot{Depot: mem}
	master := depot.NewMasterDepot([]depot.Depot{noLink})

	s := createTestServer(t, master)
	s.linksContent = true
	s.SetLinksContentThreshold(100)

	contentStr := "some data in memory depot without linked content support"
	chunks, err := master.StoreChunks(ctx, "test-obj-uuid", strings.NewReader(contentStr))
	require.NoError(t, err)
	chunksJSON, err := json.Marshal(chunks)
	require.NoError(t, err)

	hash := entities.Hash{5, 6, 7, 8}
	s.headers[hash[0]].Add(hash, newCachedHeader(&ObjectHeader{Id: hash, Size: 500, Type: "BLOB"}))
	s.content[hash[0]].Add(hash, &ObjectContent{Encoding: "zstd", Content: string(chunksJSON)})

	// Even though Size (500) > threshold (100), because depot doesn't support LinkedContent,
	// GetObject cleanly falls back to normal streaming with original encoding "zstd".
	r, err := s.GetObject(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, "zstd", r.Encoding())
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, contentStr, string(data))
	r.Close()
}

func TestSupportsLinksContentVersionCheck(t *testing.T) {
	s := &GCPServer{linksContent: true}
	s.SetLinksContentThreshold(100)

	// No metadata in context -> false
	assert.False(t, s.supportsLinksContent(context.Background()))

	// client-version <= 0.1.3 -> false
	ctxOld := metadata.NewIncomingContext(context.Background(), metadata.Pairs("client-version", "0.1.2"))
	assert.False(t, s.supportsLinksContent(ctxOld))

	ctx16 := metadata.NewIncomingContext(context.Background(), metadata.Pairs("client-version", "0.1.3"))
	assert.True(t, s.supportsLinksContent(ctx16))

	// client-version > 0.1.3 -> true (Incoming)
	ctxNew := metadata.NewIncomingContext(context.Background(), metadata.Pairs("client-version", "0.1.4"))
	assert.True(t, s.supportsLinksContent(ctxNew))

	// client-version > 0.1.3 -> true (Outgoing fallback)
	ctxOut := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("client-version", "0.1.5"))
	assert.True(t, s.supportsLinksContent(ctxOut))

	// client-version with leading v -> true
	ctxV := metadata.NewIncomingContext(context.Background(), metadata.Pairs("client-version", "v0.1.5"))
	assert.True(t, s.supportsLinksContent(ctxV))

	// mixed-case Client-Version -> true
	ctxUpper := metadata.NewIncomingContext(context.Background(), metadata.Pairs("Client-Version", "0.1.6"))
	assert.True(t, s.supportsLinksContent(ctxUpper))
}

func TestCachedHeaderSizeAndRoundTrip(t *testing.T) {
	assert.Equal(t, uintptr(16), unsafe.Sizeof(CachedHeader{}))
	assert.False(t, CachedHeader{}.Valid)

	types := []string{
		string(entities.ObjectTypeBlob),
		string(gitobjects.ObjectTypeDir),
		string(gitobjects.ObjectTypeCommit),
		string(gitobjects.ObjectTypeTag),
		string(gitobjects.ObjectTypeSymRef),
		string(gitobjects.ObjectTypeDeepTree),
		string(objects.ObjectTypeDeepTreeRequest),
		"BLOB",
		"custom-future-type",
		"",
	}

	hash := entities.Hash{9, 8, 7, 6}
	for i, objType := range types {
		orig := &ObjectHeader{
			Id:   hash,
			Size: int64(1024 * (i + 1)),
			Type: objType,
		}
		cached := newCachedHeader(orig)
		assert.True(t, cached.Valid)
		assert.Equal(t, objType, cached.TypeString())
		restored := cached.ObjectHeader(hash)
		assert.Equal(t, orig, restored)
	}
}

func TestCachedHeaderLRUHeapSavings(t *testing.T) {
	const numHeaders = 1_000_000

	measurePointerCache := func() (uint64, uint64) {
		runtime.GC()
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)

		shards := make([]*lru.Cache[entities.Hash, *ObjectHeader], 256)
		for i := range shards {
			var err error
			shards[i], err = lru.New[entities.Hash, *ObjectHeader](numHeaders/256 + 16)
			require.NoError(t, err)
		}
		for i := 0; i < numHeaders; i++ {
			var h entities.Hash
			h[0] = byte(i & 0xff)
			binary.LittleEndian.PutUint64(h[1:9], uint64(i))
			shards[h[0]].Add(h, &ObjectHeader{
				Id:   h,
				Size: int64(i),
				Type: string(entities.ObjectTypeBlob),
			})
		}

		runtime.GC()
		runtime.GC()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(shards)
		return after.HeapAlloc - before.HeapAlloc, after.HeapObjects - before.HeapObjects
	}

	measureValueCache := func() (uint64, uint64) {
		runtime.GC()
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)

		shards := make([]*lru.Cache[entities.Hash, CachedHeader], 256)
		for i := range shards {
			var err error
			shards[i], err = lru.New[entities.Hash, CachedHeader](numHeaders/256 + 16)
			require.NoError(t, err)
		}
		for i := 0; i < numHeaders; i++ {
			var h entities.Hash
			h[0] = byte(i & 0xff)
			binary.LittleEndian.PutUint64(h[1:9], uint64(i))
			shards[h[0]].Add(h, newCachedHeader(&ObjectHeader{
				Id:   h,
				Size: int64(i),
				Type: string(entities.ObjectTypeBlob),
			}))
		}

		runtime.GC()
		runtime.GC()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(shards)
		return after.HeapAlloc - before.HeapAlloc, after.HeapObjects - before.HeapObjects
	}

	beforeBytes, beforeObjects := measurePointerCache()
	afterBytes, afterObjects := measureValueCache()

	savedBytes := int64(beforeBytes) - int64(afterBytes)
	savedObjects := int64(beforeObjects) - int64(afterObjects)
	projectedGiBAt100M := float64(savedBytes) * 100.0 / (1024 * 1024 * 1024)

	t.Logf("Before (lru.Cache[Hash, *ObjectHeader]): %.2f MiB retained (%d heap objects) for %d headers",
		float64(beforeBytes)/(1024*1024), beforeObjects, numHeaders)
	t.Logf("After  (lru.Cache[Hash, CachedHeader]):  %.2f MiB retained (%d heap objects) for %d headers",
		float64(afterBytes)/(1024*1024), afterObjects, numHeaders)
	t.Logf("Saved per 1M headers:                    %.2f MiB (%d fewer heap objects; %.2f GiB projected at 100M cached headers)",
		float64(savedBytes)/(1024*1024), savedObjects, projectedGiBAt100M)

	// Each *ObjectHeader is 48 bytes on the heap (45.78 MiB per 1M entries), while
	// CachedHeader (16B) fits inside the existing 8B of unused allocator padding in lru.Entry (96B size class).
	assert.GreaterOrEqual(t, savedBytes, int64(40*1024*1024), "expected at least 40 MiB saved per 1M cached headers")
	assert.GreaterOrEqual(t, savedObjects, int64(numHeaders-1000), "expected ~1M fewer heap objects per 1M cached headers")
}

func TestHeadersCacheHitZeroObjectHeaderAllocs(t *testing.T) {
	mem := depot.NewMemoryDepot("mem", 1000000)
	master := depot.NewMasterDepot([]depot.Depot{mem})
	s := createTestServer(t, master)

	ctx := context.Background()
	keys := make([]entities.Hash, 32)
	for i := range keys {
		keys[i][0] = byte(i)
		keys[i][1] = 42
		s.headers[keys[i][0]].Add(keys[i], newCachedHeader(&ObjectHeader{
			Id:   keys[i],
			Size: int64(100 + i),
			Type: string(entities.ObjectTypeBlob),
		}))
	}

	// Single getHeader lookup on a cache hit returns CachedHeader by value with 0 heap allocations.
	singleAllocs := testing.AllocsPerRun(100, func() {
		hdr, err := s.getHeader(ctx, keys[0])
		if err != nil || !hdr.Valid || hdr.Size != 100 {
			t.Fatal("unexpected header")
		}
	})
	assert.Equal(t, float64(0), singleAllocs, "expected 0 heap allocations for cached s.getHeader")

	// Batch s.Headers lookup on 32 cached keys allocates only the 2 result slices ([]CachedHeader and []entities.ObjectHeader),
	// and 0 per-item *ObjectHeader heap structs.
	batchAllocs := testing.AllocsPerRun(100, func() {
		hdrs, err := s.Headers(ctx, keys)
		if err != nil || len(hdrs) != len(keys) || hdrs[0].Size != 100 {
			t.Fatal("unexpected headers")
		}
	})
	assert.LessOrEqual(t, batchAllocs, float64(3), "expected no per-item *ObjectHeader allocations on batch cache hit")
}

type fakeSpannerServer struct {
	spannerpb.UnimplementedSpannerServer

	mu            sync.Mutex
	nextSession   int
	nextTx        int
	nextProjectID int64
	projects      map[[2]string]int64
	refs          map[[2]any]spanner.NullString
}

func newFakeSpannerServer() *fakeSpannerServer {
	return &fakeSpannerServer{
		projects: make(map[[2]string]int64),
		refs:     make(map[[2]any]spanner.NullString),
	}
}

func (f *fakeSpannerServer) CreateSession(_ context.Context, req *spannerpb.CreateSessionRequest) (*spannerpb.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSession++
	return &spannerpb.Session{
		Name:        fmt.Sprintf("%s/sessions/s-%d", req.GetDatabase(), f.nextSession),
		Multiplexed: req.GetSession().GetMultiplexed(),
	}, nil
}

func (f *fakeSpannerServer) BatchCreateSessions(_ context.Context, req *spannerpb.BatchCreateSessionsRequest) (*spannerpb.BatchCreateSessionsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := int(req.GetSessionCount())
	if count <= 0 {
		count = 1
	}
	sessions := make([]*spannerpb.Session, count)
	for i := range count {
		f.nextSession++
		sessions[i] = &spannerpb.Session{
			Name: fmt.Sprintf("%s/sessions/s-%d", req.GetDatabase(), f.nextSession),
		}
	}
	return &spannerpb.BatchCreateSessionsResponse{Session: sessions}, nil
}

func (f *fakeSpannerServer) GetSession(_ context.Context, req *spannerpb.GetSessionRequest) (*spannerpb.Session, error) {
	return &spannerpb.Session{Name: req.GetName()}, nil
}

func (f *fakeSpannerServer) DeleteSession(_ context.Context, _ *spannerpb.DeleteSessionRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (f *fakeSpannerServer) BeginTransaction(_ context.Context, _ *spannerpb.BeginTransactionRequest) (*spannerpb.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTx++
	return &spannerpb.Transaction{Id: []byte(fmt.Sprintf("tx-%d", f.nextTx))}, nil
}

func (f *fakeSpannerServer) Rollback(_ context.Context, _ *spannerpb.RollbackRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (f *fakeSpannerServer) ExecuteStreamingSql(req *spannerpb.ExecuteSqlRequest, stream spannerpb.Spanner_ExecuteStreamingSqlServer) error {
	f.mu.Lock()
	var txMeta *spannerpb.Transaction
	if req.GetTransaction().GetBegin() != nil {
		f.nextTx++
		txMeta = &spannerpb.Transaction{Id: []byte(fmt.Sprintf("tx-%d", f.nextTx))}
	}

	sql := req.GetSql()
	fields := req.GetParams().GetFields()

	switch {
	case strings.HasPrefix(sql, "SELECT ProjectId FROM Projects"):
		key := [2]string{fields["host"].GetStringValue(), fields["project"].GetStringValue()}
		id, ok := f.projects[key]
		f.mu.Unlock()
		var values []*structpb.Value
		if ok {
			values = []*structpb.Value{structpb.NewStringValue(strconv.FormatInt(id, 10))}
		}
		return stream.Send(&spannerpb.PartialResultSet{
			Metadata: &spannerpb.ResultSetMetadata{
				Transaction: txMeta,
				RowType: &spannerpb.StructType{
					Fields: []*spannerpb.StructType_Field{
						{Name: "ProjectId", Type: &spannerpb.Type{Code: spannerpb.TypeCode_INT64}},
					},
				},
			},
			Values: values,
		})

	case strings.HasPrefix(sql, "INSERT INTO Projects"):
		key := [2]string{fields["host"].GetStringValue(), fields["project"].GetStringValue()}
		if _, exists := f.projects[key]; exists {
			f.mu.Unlock()
			return status.Error(codes.AlreadyExists, "project already exists")
		}
		f.nextProjectID++
		id := f.nextProjectID
		f.projects[key] = id
		f.mu.Unlock()
		return stream.Send(&spannerpb.PartialResultSet{
			Metadata: &spannerpb.ResultSetMetadata{
				Transaction: txMeta,
				RowType: &spannerpb.StructType{
					Fields: []*spannerpb.StructType_Field{
						{Name: "ProjectId", Type: &spannerpb.Type{Code: spannerpb.TypeCode_INT64}},
					},
				},
			},
			Values: []*structpb.Value{structpb.NewStringValue(strconv.FormatInt(id, 10))},
		})

	case strings.Contains(sql, "UNNEST(@refs) AS r"):
		projectID, err := strconv.ParseInt(fields["projectId"].GetStringValue(), 10, 64)
		if err != nil {
			f.mu.Unlock()
			return status.Errorf(codes.InvalidArgument, "invalid projectId: %v", err)
		}
		var values []*structpb.Value
		for _, v := range fields["refs"].GetListValue().GetValues() {
			refName := v.GetStringValue()
			target, exists := f.refs[[2]any{projectID, refName}]
			if !exists {
				continue
			}
			values = append(values, structpb.NewStringValue(refName))
			if target.Valid {
				values = append(values, structpb.NewStringValue(target.StringVal))
			} else {
				values = append(values, structpb.NewNullValue())
			}
		}
		f.mu.Unlock()
		return stream.Send(&spannerpb.PartialResultSet{
			Metadata: &spannerpb.ResultSetMetadata{
				Transaction: txMeta,
				RowType: &spannerpb.StructType{
					Fields: []*spannerpb.StructType_Field{
						{Name: "Ref", Type: &spannerpb.Type{Code: spannerpb.TypeCode_STRING}},
						{Name: "Target", Type: &spannerpb.Type{Code: spannerpb.TypeCode_STRING}},
					},
				},
			},
			Values: values,
		})

	default:
		f.mu.Unlock()
		return status.Errorf(codes.Unimplemented, "unsupported SQL: %s", sql)
	}
}

func (f *fakeSpannerServer) Commit(_ context.Context, req *spannerpb.CommitRequest) (*spannerpb.CommitResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, m := range req.GetMutations() {
		iou := m.GetInsertOrUpdate()
		if iou == nil || iou.GetTable() != "Refs" {
			return nil, status.Errorf(codes.InvalidArgument, "unexpected mutation: %v", m)
		}
		for _, row := range iou.GetValues() {
			vals := row.GetValues()
			projectID, err := strconv.ParseInt(vals[0].GetStringValue(), 10, 64)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid ProjectId: %v", err)
			}
			refName := vals[1].GetStringValue()
			var target spanner.NullString
			if _, isNull := vals[3].GetKind().(*structpb.Value_NullValue); !isNull {
				target = spanner.NullString{StringVal: vals[3].GetStringValue(), Valid: true}
			}
			f.refs[[2]any{projectID, refName}] = target
		}
	}
	return &spannerpb.CommitResponse{CommitTimestamp: timestamppb.Now()}, nil
}

func createTestServerWithCASDB(t *testing.T) (*GCPServer, *fakeSpannerServer) {
	t.Helper()
	ctx := context.Background()

	// Start in-memory Bigtable server.
	btSrv, err := bttest.NewServer("localhost:0")
	require.NoError(t, err)
	t.Cleanup(btSrv.Close)

	btConn, err := grpc.NewClient(btSrv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = btConn.Close() })

	btAdmin, err := bigtable.NewAdminClient(ctx, "test-proj", "test-bt", option.WithGRPCConn(btConn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = btAdmin.Close() })
	require.NoError(t, btAdmin.CreateTable(ctx, "objects"))
	require.NoError(t, btAdmin.CreateColumnFamily(ctx, "objects", "meta"))
	require.NoError(t, btAdmin.CreateColumnFamily(ctx, "objects", "data"))

	// Start in-memory Spanner server.
	fakeSpanner := newFakeSpannerServer()
	spLis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	spSrv := grpc.NewServer()
	spannerpb.RegisterSpannerServer(spSrv, fakeSpanner)
	go func() { _ = spSrv.Serve(spLis) }()
	t.Cleanup(func() {
		spSrv.Stop()
		_ = spLis.Close()
	})

	t.Setenv("BIGTABLE_EMULATOR_HOST", btSrv.Addr)
	t.Setenv("SPANNER_EMULATOR_HOST", spLis.Addr().String())

	db, err := casdb.New(ctx, "projects/test-proj/instances/test-inst/databases/test-db", "test-proj", "test-bt", "", 1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mem := depot.NewMemoryDepot("mem", 1000000)
	master := depot.NewMasterDepot([]depot.Depot{mem})
	s := createTestServer(t, master)
	s.casdb = db
	s.addObject = db.AddObject
	s.projectsByName = make(map[[2]string]int64)
	s.projectsByID = make(map[int64][2]string)
	return s, fakeSpanner
}

func TestXchgRefsEmptyShortCircuit(t *testing.T) {
	// With s.casdb == nil and s.projectsByName == nil, an empty XchgRefs call must
	// return nil immediately without attempting to look up or create a project.
	s := &GCPServer{}
	require.NoError(t, s.XchgRefs(context.Background(), "server", "project", nil))
	require.NoError(t, s.XchgRefs(context.Background(), "server", "project", []remote.RefXchg{}))
}

func TestGCPServerXchgRefAndXchgRefs(t *testing.T) {
	ctx := context.Background()
	s, _ := createTestServerWithCASDB(t)

	hash1 := entities.NewHash("1111111111111111111111111111111111111111")
	hash2 := entities.NewHash("2222222222222222222222222222222222222222")
	missingHash := entities.NewHash("9999999999999999999999999999999999999999")

	// Seed hash1 and hash2 in the server's header cache so link validation succeeds for them.
	s.headers[hash1[0]].Add(hash1, newCachedHeader(&ObjectHeader{Id: hash1, Size: 10, Type: string(entities.ObjectTypeBlob)}))
	s.headers[hash2[0]].Add(hash2, newCachedHeader(&ObjectHeader{Id: hash2, Size: 20, Type: string(entities.ObjectTypeBlob)}))

	// 1. XchgRef with missing Desired object fails link validation with ErrObjectLinkValidation.
	err := s.XchgRef(ctx, "srv", "proj", "refs/heads/main", missingHash, entities.ZeroHash)
	var linkErr *remote.ErrObjectLinkValidation
	require.ErrorAs(t, err, &linkErr)
	assert.Equal(t, []entities.Hash{missingHash}, linkErr.MissingLinks)

	// 2. XchgRef from ZeroHash -> hash1 succeeds.
	require.NoError(t, s.XchgRef(ctx, "srv", "proj", "refs/heads/main", hash1, entities.ZeroHash))
	got, err := s.GetRefs(ctx, "srv", "proj", []string{"refs/heads/main"})
	require.NoError(t, err)
	assert.Equal(t, []entities.Hash{hash1}, got)

	// 3. XchgRef with mismatched Current fails with ErrRefMismatch.
	err = s.XchgRef(ctx, "srv", "proj", "refs/heads/main", hash2, entities.ZeroHash)
	var mismatchErr *remote.ErrRefMismatch
	require.ErrorAs(t, err, &mismatchErr)
	assert.Equal(t, "refs/heads/main", mismatchErr.Ref)
	assert.Equal(t, entities.ZeroHash, mismatchErr.Current)
	assert.Equal(t, hash1, mismatchErr.Actual)

	// 4. XchgRefs batch updating refs/heads/main (hash1 -> hash2) and creating refs/heads/dev (ZeroHash -> hash1).
	err = s.XchgRefs(ctx, "srv", "proj", []remote.RefXchg{
		{Ref: "refs/heads/main", Current: hash1, Desired: hash2},
		{Ref: "refs/heads/dev", Current: entities.ZeroHash, Desired: hash1},
	})
	require.NoError(t, err)

	got, err = s.GetRefs(ctx, "srv", "proj", []string{"refs/heads/main", "refs/heads/dev"})
	require.NoError(t, err)
	assert.Equal(t, []entities.Hash{hash2, hash1}, got)

	// 5. XchgRef deleting a ref (Desired == ZeroHash) skips link validation for ZeroHash and tombstones the ref.
	require.NoError(t, s.XchgRef(ctx, "srv", "proj", "refs/heads/dev", entities.ZeroHash, hash1))
	got, err = s.GetRefs(ctx, "srv", "proj", []string{"refs/heads/dev"})
	require.NoError(t, err)
	assert.Equal(t, []entities.Hash{entities.ZeroHash}, got)
}

func TestFilterServerXchgRefAndXchgRefs(t *testing.T) {
	s, _ := createTestServerWithCASDB(t)
	hash1 := entities.NewHash("1111111111111111111111111111111111111111")
	s.headers[hash1[0]].Add(hash1, newCachedHeader(&ObjectHeader{Id: hash1, Size: 10, Type: string(entities.ObjectTypeBlob)}))

	filterServer := NewFilterServer(s, regexp.MustCompile(`^10\.0\.0\.99:`))

	blockedCtx := peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.99"), Port: 1234},
	})
	allowedCtx := peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1234},
	})

	// Blocked peer is rejected on both XchgRef and XchgRefs.
	require.ErrorContains(t, filterServer.XchgRef(blockedCtx, "srv", "proj", "refs/heads/main", hash1, entities.ZeroHash), "cannot serve")
	require.ErrorContains(t, filterServer.XchgRefs(blockedCtx, "srv", "proj", []remote.RefXchg{
		{Ref: "refs/heads/main", Current: entities.ZeroHash, Desired: hash1},
	}), "cannot serve")

	// Allowed peer succeeds.
	require.NoError(t, filterServer.XchgRef(allowedCtx, "srv", "proj", "refs/heads/main", hash1, entities.ZeroHash))
	require.NoError(t, filterServer.XchgRefs(allowedCtx, "srv", "proj", []remote.RefXchg{
		{Ref: "refs/heads/main", Current: hash1, Desired: entities.ZeroHash},
	}))
}

func TestPutObject_FailedAddObjectKeepsCommittedContent(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	s, _ := createTestServerWithCASDB(t)

	// The mutation is applied but the call still fails, as when the response is lost.
	s.addObject = func(ctx context.Context, hdr *ObjectHeader, content *ObjectContent) error {
		require.NoError(s.casdb.AddObject(ctx, hdr, content))
		return errors.New("response lost")
	}
	content := []byte("blob 11\x00hello world")
	key := objects.HashBlobBytes([]byte("hello world"))
	err := s.PutObject(ctx, key, remote.NewContentReader(io.NopCloser(bytes.NewReader(content)), remote.EncodingRaw))
	require.Error(err)

	readable := func() bool {
		r, err := s.GetObject(ctx, key)
		if err != nil {
			return false
		}
		defer r.Close()
		_, err = io.ReadAll(r)
		return err == nil
	}
	require.Never(func() bool { return !readable() }, 500*time.Millisecond, 10*time.Millisecond)
}

// recordingDepot records the chunks it removes.
type recordingDepot struct {
	depot.Depot
	mu      sync.Mutex
	removed []string
}

func (d *recordingDepot) Remove(ctx context.Context, c depot.Chunk) error {
	d.mu.Lock()
	d.removed = append(d.removed, c.ID)
	d.mu.Unlock()
	return d.Depot.Remove(ctx, c)
}

func (d *recordingDepot) removedCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.removed)
}

func newRecordingServer(t *testing.T) (*GCPServer, *recordingDepot) {
	t.Helper()
	s, _ := createTestServerWithCASDB(t)
	rec := &recordingDepot{Depot: depot.NewMemoryDepot("mem", 1000000)}
	s.depot = depot.NewMasterDepot([]depot.Depot{rec})
	return s, rec
}

func TestPutObject_AttemptedAddObjectKeepsContent(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	s, rec := newRecordingServer(t)

	// Whether the row was written is unknown, so the content must stay.
	s.addObject = func(ctx context.Context, hdr *ObjectHeader, content *ObjectContent) error {
		return errors.New("write failed")
	}
	content := []byte("blob 11\x00hello again")
	key := objects.HashBlobBytes([]byte("hello again"))
	err := s.PutObject(ctx, key, remote.NewContentReader(io.NopCloser(bytes.NewReader(content)), remote.EncodingRaw))
	require.Error(err)
	require.Never(func() bool { return rec.removedCount() > 0 }, 500*time.Millisecond, 10*time.Millisecond)
}

func TestPutObject_FailureBeforeAddObjectRemovesContent(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	s, rec := newRecordingServer(t)

	// The content does not hash to the key, so PutObject fails before AddObject.
	content := []byte("blob 11\x00hello again")
	key := objects.HashBlobBytes([]byte("other content"))
	err := s.PutObject(ctx, key, remote.NewContentReader(io.NopCloser(bytes.NewReader(content)), remote.EncodingRaw))
	require.ErrorContains(err, "expected hash")
	require.Eventually(func() bool { return rec.removedCount() == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestSupportsLinksContent_OffUnlessEnabled(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("client-version", "0.1.23"))

	s := &GCPServer{}
	s.SetLinksContentThreshold(100)
	assert.False(t, s.supportsLinksContent(ctx))

	s.linksContent = true
	assert.True(t, s.supportsLinksContent(ctx))

	// A zero threshold returns links for every object; it does not turn links off.
	s.SetLinksContentThreshold(0)
	assert.True(t, s.supportsLinksContent(ctx))
}

func TestSearchRefs_ConcurrentWithProjectCacheWrites(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	s := newEmulatorCASDBServer(t)

	target := entities.NewHash("1111111111111111111111111111111111111111")
	s.headers[target[0]].Add(target, newCachedHeader(&ObjectHeader{Id: target, Size: 10, Type: string(entities.ObjectTypeBlob)}))
	require.NoError(s.PutRefs(ctx, "race.example", "seed", []remote.RefHash{{Ref: "refs/heads/r", Hash: target}}))

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := int64(1); ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// What projectForRefs does when another request creates a project.
			s.projectIDsLock.Lock()
			s.projectsByID[-i] = [2]string{"race.example", fmt.Sprintf("p%d", i)}
			s.projectsByName[[2]string{"race.example", fmt.Sprintf("p%d", i)}] = -i
			s.projectIDsLock.Unlock()
		}
	}()
	for range 50 {
		refs, err := s.SearchRefs(ctx, "race.example", "", "refs/heads/r", 0)
		require.NoError(err)
		require.Len(refs, 1)
	}
	close(stop)
	<-done
}
