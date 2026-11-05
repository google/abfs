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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/mod/semver"
	"google.golang.org/grpc/metadata"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers"

	"github.com/google/abfs/shared/remote/servers/gcp/casdb"
	"github.com/google/abfs/shared/remote/servers/gcp/depot"
)

type (
	ObjectHeader  = casdb.ObjectHeader
	ObjectContent = casdb.ObjectContent
	Server        = GCPServer
)

// CachedHeader is a compact representation of casdb.ObjectHeader
// stored by value in the s.headers LRU cache.  It does not duplicate
// the 20-byte hash. It reduces heap allocations and GC pointer scans.
type CachedHeader struct {
	Size int64
	// Passing for up to 2^16 unique object types
	Type  uint16
	Valid bool
}

const (
	cachedObjTypeUnknown uint16 = iota
	cachedObjTypeBlob
	cachedObjTypeDir
	cachedObjTypeCommit
	cachedObjTypeTag
	cachedObjTypeSymRef
	cachedObjTypeDeepTree
	cachedObjTypeDeepTreeRequest
	cachedObjTypeFirstCustom
)

var (
	customObjTypesMu sync.RWMutex
	customObjTypes   []string
	customObjTypeIDs = make(map[string]uint16)
)

func encodeObjectType(t string) uint16 {
	switch t {
	case string(entities.ObjectTypeBlob):
		return cachedObjTypeBlob
	case string(gitobjects.ObjectTypeDir):
		return cachedObjTypeDir
	case string(gitobjects.ObjectTypeCommit):
		return cachedObjTypeCommit
	case string(gitobjects.ObjectTypeTag):
		return cachedObjTypeTag
	case string(gitobjects.ObjectTypeSymRef):
		return cachedObjTypeSymRef
	case string(gitobjects.ObjectTypeDeepTree):
		return cachedObjTypeDeepTree
	case string(objects.ObjectTypeDeepTreeRequest):
		return cachedObjTypeDeepTreeRequest
	case "":
		return cachedObjTypeUnknown
	}

	// Fall through for newly introduced types, but it is preferred
	// for them to exist above.
	customObjTypesMu.RLock()
	id, ok := customObjTypeIDs[t]
	customObjTypesMu.RUnlock()
	if ok {
		return id
	}

	customObjTypesMu.Lock()
	defer customObjTypesMu.Unlock()
	if id, ok := customObjTypeIDs[t]; ok {
		return id
	}
	nextID := int(cachedObjTypeFirstCustom) + len(customObjTypes)
	if nextID > 65535 {
		return cachedObjTypeUnknown
	}
	id = uint16(nextID)
	customObjTypes = append(customObjTypes, t)
	customObjTypeIDs[t] = id
	return id
}

func decodeObjectType(id uint16) string {
	switch id {
	case cachedObjTypeBlob:
		return string(entities.ObjectTypeBlob)
	case cachedObjTypeDir:
		return string(gitobjects.ObjectTypeDir)
	case cachedObjTypeCommit:
		return string(gitobjects.ObjectTypeCommit)
	case cachedObjTypeTag:
		return string(gitobjects.ObjectTypeTag)
	case cachedObjTypeSymRef:
		return string(gitobjects.ObjectTypeSymRef)
	case cachedObjTypeDeepTree:
		return string(gitobjects.ObjectTypeDeepTree)
	case cachedObjTypeDeepTreeRequest:
		return string(objects.ObjectTypeDeepTreeRequest)
	case cachedObjTypeUnknown:
		return ""
	default:
		idx := int(id) - int(cachedObjTypeFirstCustom)
		customObjTypesMu.RLock()
		defer customObjTypesMu.RUnlock()
		if idx >= 0 && idx < len(customObjTypes) {
			return customObjTypes[idx]
		}
		return ""
	}
}

func newCachedHeader(hdr *ObjectHeader) CachedHeader {
	return CachedHeader{
		Size:  hdr.Size,
		Type:  encodeObjectType(hdr.Type),
		Valid: true,
	}
}

func (h CachedHeader) TypeString() string {
	return decodeObjectType(h.Type)
}

func (h CachedHeader) ObjectHeader(id entities.Hash) *ObjectHeader {
	if !h.Valid {
		return nil
	}
	return &ObjectHeader{
		Id:   id,
		Size: h.Size,
		Type: decodeObjectType(h.Type),
	}
}

const (
	minVersionLinksContent       = "0.1.3"
	DefaultMaxCachedHeaders      = 100 * 1000 * 1000
	DefaultMaxCachedContents     = 1 * 1000 * 1000
	DefaultLinksContentThreshold = 1 * 1024 * 1024
	// DefaultBtConnPool is the default Bigtable client gRPC connection pool size.
	DefaultBtConnPool = 50
	cleanupTimeout    = 10 * time.Second
)

// This is the gcpserver layer version
// The canonical abfs repo has a remote layer with separate versioning
// filled in through the makefile
var version = ""

var _ remote.RemoteCloser = (*GCPServer)(nil)

type GCPServer struct {
	casdb                 *casdb.CASDB
	addObject             func(ctx context.Context, hdr *ObjectHeader, content *ObjectContent) error
	depot                 *depot.MasterDepot
	storeLinks            bool
	validateHash          bool
	linksContentThreshold int64

	headers        []*lru.Cache[entities.Hash, CachedHeader]
	content        []*lru.Cache[entities.Hash, *ObjectContent]
	projectIDsLock sync.RWMutex
	projectsByName map[[2]string]int64
	projectsByID   map[int64][2]string

	headersReqCount       atomic.Int64
	headersRespCount      atomic.Int64
	headersCacheMissCount atomic.Int64
	headersErrCount       atomic.Int64
	headersMissCount      atomic.Int64
}

type NewServerOpts struct {
	ProjectID    string
	InstanceID   string
	DatabaseID   string
	BtInstance   string
	BtAppProfile string
	// BtConnPool sets the Bigtable gRPC connection pool size. A non-positive
	// value uses DefaultBtConnPool.
	BtConnPool            int
	BucketName            string
	MaxCachedHeaders      int
	MaxCachedContents     int
	LinksContentThreshold int64
}

func New(ctx context.Context, opts *NewServerOpts) (*GCPServer, error) {
	spannerDB := fmt.Sprintf("projects/%s/instances/%s/databases/%s", opts.ProjectID, opts.InstanceID, opts.DatabaseID)

	btConnPool := opts.BtConnPool
	if btConnPool <= 0 {
		btConnPool = DefaultBtConnPool
	}
	cas, err := casdb.New(ctx, spannerDB, opts.ProjectID, opts.BtInstance, opts.BtAppProfile, btConnPool)
	if err != nil {
		return nil, err
	}
	maxCachedHeaders := opts.MaxCachedHeaders
	if maxCachedHeaders == 0 {
		maxCachedHeaders = DefaultMaxCachedHeaders
	}
	maxCachedContents := opts.MaxCachedContents
	if maxCachedContents == 0 {
		maxCachedContents = DefaultMaxCachedContents
	}
	linksThreshold := opts.LinksContentThreshold
	if linksThreshold == 0 {
		linksThreshold = DefaultLinksContentThreshold
	}
	s := &GCPServer{
		headers:               make([]*lru.Cache[entities.Hash, CachedHeader], 256),
		content:               make([]*lru.Cache[entities.Hash, *ObjectContent], 256),
		projectsByName:        make(map[[2]string]int64),
		projectsByID:          make(map[int64][2]string),
		storeLinks:            false,
		validateHash:          true,
		linksContentThreshold: linksThreshold,
		casdb:                 cas,
		addObject:             cas.AddObject,
	}
	for i := range s.headers {
		s.headers[i], err = lru.New[entities.Hash, CachedHeader](maxCachedHeaders / 256)
		if err != nil {
			return nil, err
		}
	}
	for i := range s.content {
		s.content[i], err = lru.New[entities.Hash, *ObjectContent](maxCachedContents / 256)
		if err != nil {
			return nil, err
		}
	}
	if err := s.setMasterDepot(ctx, opts.BucketName); err != nil {
		// Clean close on error
		return nil, err
	}
	return s, nil
}

func (s *GCPServer) SetLinksContentThreshold(threshold int64) {
	s.linksContentThreshold = threshold
}

func (s *GCPServer) RegisterHTTPRoutes(r *mux.Router) {
	r.Path("/server/status").Methods("GET").HandlerFunc(s.handleGetStatus)
}

func lenCaches[K comparable, V any](c []*lru.Cache[K, V]) int64 {
	var sum int64
	for _, v := range c {
		sum += int64(v.Len())
	}
	return sum
}

func (s *GCPServer) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	err := json.NewEncoder(w).Encode(struct {
		CachedHeaders         int64
		CachedContent         int64
		HeadersReqCount       int64
		HeadersRespCount      int64
		HeadersCacheMissCount int64
		HeadersErrCount       int64
		HeadersMissCount      int64
	}{
		lenCaches(s.headers),
		lenCaches(s.content),
		s.headersReqCount.Load(),
		s.headersRespCount.Load(),
		s.headersCacheMissCount.Load(),
		s.headersErrCount.Load(),
		s.headersMissCount.Load(),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// Headers implements remote.RemoteProvider.
func (s *GCPServer) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	headers, err := s.getHeaders(ctx, keys)
	if err != nil {
		return nil, err
	}

	result := make([]entities.ObjectHeader, len(keys))
	for i, obj := range headers {
		if obj.Valid {
			result[i].Size = obj.Size
			result[i].Type = entities.ObjectType(obj.TypeString())
		}
	}
	return result, nil
}

func (s *GCPServer) supportsLinksContent(ctx context.Context) bool {
	if s.linksContentThreshold == 0 {
		return false
	}
	var clientVersion string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("client-version"); len(vals) > 0 {
			clientVersion = vals[0]
		}
	}
	if clientVersion == "" {
		if md, ok := metadata.FromOutgoingContext(ctx); ok {
			if vals := md.Get("client-version"); len(vals) > 0 {
				clientVersion = vals[0]
			}
		}
	}
	if clientVersion == "" {
		return false
	}
	cleanVer := "v" + strings.TrimPrefix(clientVersion, "v")
	return semver.Compare(cleanVer, "v"+minVersionLinksContent) >= 0
}

func (s *GCPServer) getObjectData(ctx context.Context, key remote.Hash) (CachedHeader, *ObjectContent, error) {
	shard := key[0]
	cachedHdr, hdrOk := s.headers[shard].Get(key)
	content, contentOk := s.content[shard].Get(key)

	switch {
	case hdrOk && contentOk:
		return cachedHdr, content, nil
	case hdrOk && !contentOk:
		var err error
		content, err = s.casdb.GetContent(ctx, key)
		if errors.Is(err, casdb.ErrNotFound) || (err == nil && content == nil) {
			s.headers[shard].Remove(key)
			return CachedHeader{}, nil, entities.ErrNotExist
		}
		if err != nil {
			return CachedHeader{}, nil, err
		}
		s.content[shard].PeekOrAdd(key, content)
		return cachedHdr, content, nil
	case !hdrOk && contentOk:
		header, err := s.casdb.GetHeader(ctx, key)
		if errors.Is(err, casdb.ErrNotFound) || (err == nil && header == nil) {
			s.content[shard].Remove(key)
			return CachedHeader{}, nil, entities.ErrNotExist
		}
		if err != nil {
			return CachedHeader{}, nil, err
		}
		cachedHdr = newCachedHeader(header)
		s.headers[shard].PeekOrAdd(key, cachedHdr)
		return cachedHdr, content, nil
	default:
		header, objContent, err := s.casdb.GetObjectData(ctx, key)
		if errors.Is(err, casdb.ErrNotFound) || (err == nil && header == nil) {
			return CachedHeader{}, nil, entities.ErrNotExist
		}
		if err != nil {
			return CachedHeader{}, nil, err
		}
		cachedHdr = newCachedHeader(header)
		content = objContent
		s.headers[shard].PeekOrAdd(key, cachedHdr)
		s.content[shard].PeekOrAdd(key, content)
		return cachedHdr, content, nil
	}
}

// GetObject implements remote.RemoteProvider.
func (s *GCPServer) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	header, content, err := s.getObjectData(ctx, key)
	if err != nil {
		return nil, err
	}

	if s.supportsLinksContent(ctx) && header.Size > s.linksContentThreshold {
		links, err := s.depot.LinkedContents(ctx, content.Content)
		if err == nil {
			linksContent := remote.LinksContent{
				Encoding: content.Encoding,
				Links:    links,
			}
			linksJSON, err := json.Marshal(linksContent)
			if err == nil {
				return remote.NewContentReader(io.NopCloser(bytes.NewReader(linksJSON)), remote.EncodingLinks), nil
			}
		}
	}

	r, err := s.depot.Load(ctx, content.Content)
	if err != nil {
		return nil, err
	}

	return remote.NewContentReader(r, content.Encoding), nil
}

// PutObject implements remote.RemoteProvider.
func (s *GCPServer) PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) (retErr error) {
	defer entities.CloseErrCheck(reader, &retErr)

	existingHdr, err := s.getHeader(ctx, key)
	if err != nil || existingHdr.Valid {
		return err
	}

	validateOpt := remote.ExtractLinksOpt(true)
	validator, err := remote.NewValidator(key, reader.Encoding(), validateOpt, remote.ValidateHashOpt(s.validateHash), remote.ExtractLinksOpt(true))
	if err != nil {
		return err
	}
	defer func() { _ = validator.Close() }()

	origReader := io.TeeReader(reader, validator)

	namePrefix := fmt.Sprintf("%s-%s", key, uuid.New().String())
	content, err := s.depot.Store(ctx, namePrefix, origReader)
	if err != nil {
		return err
	}
	addAttempted := false
	defer func() {
		if retErr == nil {
			return
		}
		if addAttempted {
			// A failed AddObject call may still have written the object row, for
			// example when the response was lost, and other replicas may have
			// read it, so the content stays for a later orphan sweep.
			log.Printf("Kept content of failed PutObject (%s): the object row may reference it", key)
			return
		}
		go s.removeFailedContent(key, content)
	}()

	if err := validator.Close(); err != nil {
		return err
	}

	links := validator.Links()
	if err := s.validateObjectLinks(ctx, links); err != nil {
		return err
	}

	header := &casdb.ObjectHeader{
		Id:   key,
		Type: validator.Type(),
		Size: validator.Size(),
	}
	objContent := &casdb.ObjectContent{
		Encoding: reader.Encoding(),
		Content:  content,
	}

	addAttempted = true
	err = s.addObject(ctx, header, objContent)
	if err == nil {
		s.headers[key[0]].PeekOrAdd(key, newCachedHeader(header))
		s.content[key[0]].PeekOrAdd(key, objContent)
	}

	return err
}

// removeFailedContent removes the content stored by a PutObject that failed
// before it tried to write the object row.
func (s *GCPServer) removeFailedContent(key entities.Hash, content string) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := s.depot.Remove(ctx, content); err != nil {
		log.Printf("Failed to remove content of failed PutObject (%s): %v", key, err)
	} else {
		log.Printf("Removed content of failed PutObject (%s) in %s", key, time.Since(start))
	}
}

// MapResults implements remote.RemoteProvider.
func (s *GCPServer) MapResults(ctx context.Context, from remote.Hash) ([]remote.Hash, error) {
	return s.casdb.MapResults(ctx, from)
}

// AddMapResult implements remote.RemoteProvider.
func (s *GCPServer) AddMapResult(ctx context.Context, from remote.Hash, to remote.Hash) error {
	hdrs, err := s.getHeaders(ctx, []entities.Hash{from, to})
	if err != nil {
		return err
	}

	if !hdrs[0].Valid || !hdrs[1].Valid {
		var missing []entities.Hash

		if !hdrs[0].Valid {
			missing = append(missing, from)
		}
		if !hdrs[1].Valid {
			missing = append(missing, to)
		}

		return &remote.ErrObjectLinkValidation{MissingLinks: missing}
	}
	return s.casdb.AddMapResult(ctx, from, to)
}

func (s *GCPServer) projectForRefs(ctx context.Context, server string, project string, createIfMissing bool) (int64, error) {
	key := [2]string{server, project}

	s.projectIDsLock.RLock()
	id, ok := s.projectsByName[key]
	s.projectIDsLock.RUnlock()
	if ok {
		return id, nil
	}

	s.projectIDsLock.Lock()
	defer s.projectIDsLock.Unlock()
	id, ok = s.projectsByName[key]
	if ok {
		return id, nil
	}

	var err error
	if createIfMissing {
		id, err = s.casdb.GetOrCreateProject(ctx, server, project)
	} else {
		id, err = s.casdb.GetProjectID(ctx, server, project)
	}

	if err != nil {
		return 0, err
	}

	s.projectsByName[key] = id
	s.projectsByID[id] = key

	return id, nil
}

// GetRefs implements remote.RemoteProvider.
func (s *GCPServer) GetRefs(ctx context.Context, server string, project string, refs []string) ([]remote.Hash, error) {
	id, err := s.projectForRefs(ctx, server, project, false)
	if errors.Is(err, casdb.ErrNotFound) {
		return nil, entities.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	return s.casdb.GetRefs(ctx, id, refs)
}

// SearchRefs implements remote.RemoteProvider.
func (s *GCPServer) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]remote.ProjectRefHash, error) {
	var projectID *int64
	if project != "" {
		id, err := s.projectForRefs(ctx, server, project, false)
		if err != nil {
			return nil, err
		}
		projectID = &id
	}

	refIDs, err := s.casdb.SearchRefs(ctx, projectID, refPrefix, limit)
	if err != nil {
		return nil, err
	}

	s.projectIDsLock.RLock()
	missingsSet := make(map[int64]struct{}, 0)
	for _, r := range refIDs {
		_, ok := s.projectsByID[r.ProjectID]
		if !ok {
			missingsSet[r.ProjectID] = struct{}{}
		}
	}
	s.projectIDsLock.RUnlock()

	if len(missingsSet) > 0 {
		missings := make([]int64, 0, len(missingsSet))
		for id := range missingsSet {
			missings = append(missings, id)
		}

		projects, err := s.casdb.GetProjectsByIDs(ctx, missings)
		if err != nil {
			return nil, err
		}
		s.projectIDsLock.Lock()
		for i, sp := range projects {
			if _, ok := s.projectsByID[missings[i]]; !ok {
				s.projectsByID[missings[i]] = sp
				s.projectsByName[sp] = missings[i]
			}
		}
		s.projectIDsLock.Unlock()
	}

	refs := make([]remote.ProjectRefHash, 0, len(refIDs))
	for _, refID := range refIDs {
		sp := s.projectsByID[refID.ProjectID]
		if sp[0] != server {
			continue
		}
		refs = append(refs, remote.ProjectRefHash{
			Project: sp[1],
			Ref:     refID.Ref,
			Hash:    refID.Hash,
		})
	}

	return refs, nil
}

// PutRefs implements remote.RemoteProvider.
func (s *GCPServer) PutRefs(ctx context.Context, server string, project string, refs []remote.RefHash) error {
	id, err := s.projectForRefs(ctx, server, project, true)
	if err != nil {
		return err
	}
	links := make([]entities.Hash, len(refs))
	for i, ref := range refs {
		links[i] = ref.Hash
	}
	if err := s.validateObjectLinks(ctx, links); err != nil {
		return err
	}
	return s.casdb.AddRefs(ctx, id, refs)
}

// XchgRef implements remote.RemoteProvider.
func (s *GCPServer) XchgRef(ctx context.Context, server string, project string, ref string, desired remote.Hash, current remote.Hash) error {
	return s.XchgRefs(ctx, server, project, []remote.RefXchg{
		{
			Ref:     ref,
			Current: current,
			Desired: desired,
		},
	})
}

// XchgRefs implements remote.RemoteProvider.
func (s *GCPServer) XchgRefs(ctx context.Context, server string, project string, refs []remote.RefXchg) error {
	if len(refs) == 0 {
		return nil
	}
	id, err := s.projectForRefs(ctx, server, project, true)
	if err != nil {
		return err
	}
	links := make([]entities.Hash, len(refs))
	for i, ref := range refs {
		links[i] = ref.Desired
	}
	if err := s.validateObjectLinks(ctx, links); err != nil {
		return err
	}
	return s.casdb.XchgRefs(ctx, id, refs)
}

// Mark implements remote.RemoteProvider.
func (s *GCPServer) Mark(ctx context.Context, hash remote.Hash, key string, value string) (int64, error) {
	return 0, errors.New("unimplemented")
}

// Version implements remote.RemoteProvider.
func (s *GCPServer) Version(ctx context.Context) (string, error) {
	if version != "" {
		return version, nil
	}
	return servers.ServerVersion, nil
}

// WhoAmI implements remote.RemoteProvider.
func (s *GCPServer) WhoAmI(ctx context.Context) (string, error) {
	return "", errors.New("unimplemented")
}

// Close implements remote.RemoteCloser.
func (s *GCPServer) Close() error {
	return s.casdb.Close()
}

func (s *GCPServer) setMasterDepot(ctx context.Context, bucketName string) error {
	btCli := s.casdb.BTClient()

	bucketCli, err := storage.NewGRPCClient(ctx)
	if err != nil {
		return err
	}

	writeDepots := []depot.Depot{
		depot.NewInlineDepot("inline", 32*1024, true),
		depot.NewBigtableDepot("bigtable", 1*1024*1024, btCli, "chunks"),
		depot.NewBucketDepot("bucket", 10*1024*1024, bucketCli, bucketName),
		depot.NewBucketDepot("bucket", 1000*1024*1024, bucketCli, bucketName),
	}

	s.depot = depot.NewMasterDepot(writeDepots)
	return nil
}

func (s *GCPServer) getHeader(ctx context.Context, hash entities.Hash) (CachedHeader, error) {
	if cached, ok := s.headers[hash[0]].Get(hash); ok {
		return cached, nil
	}
	obj, err := s.casdb.GetHeader(ctx, hash)
	if err == casdb.ErrNotFound {
		return CachedHeader{}, nil
	}
	if err != nil || obj == nil {
		return CachedHeader{}, err
	}
	cached := newCachedHeader(obj)
	s.headers[hash[0]].PeekOrAdd(hash, cached)
	return cached, nil
}

func (s *GCPServer) getHeaders(ctx context.Context, hashes []entities.Hash) ([]CachedHeader, error) {
	result := make([]CachedHeader, len(hashes))
	var misses []entities.Hash
	missesIdx := make(map[entities.Hash][]int)
	s.headersReqCount.Add(int64(len(hashes)))
	missCount := 0
	for i, h := range hashes {
		if cached, ok := s.headers[h[0]].Get(h); ok {
			result[i] = cached
			s.headersRespCount.Add(1)
		} else if m, ok := missesIdx[h]; ok {
			missesIdx[h] = append(m, i)
			missCount++
		} else {
			misses = append(misses, h)
			missesIdx[h] = []int{i}
			missCount++
		}
	}

	if missCount == 0 {
		return result, nil
	}

	s.headersCacheMissCount.Add(int64(missCount))

	objs, err := s.casdb.GetHeaders(ctx, misses)
	if err != nil {
		s.headersErrCount.Add(int64(missCount))
		return nil, err
	}

	foundCount := 0
	for _, o := range objs {
		cached := newCachedHeader(o)
		s.headers[o.Id[0]].PeekOrAdd(o.Id, cached)
		for _, i := range missesIdx[o.Id] {
			foundCount++
			result[i] = cached
		}
	}

	s.headersRespCount.Add(int64(foundCount))
	s.headersMissCount.Add(int64(missCount - foundCount))

	return result, nil
}

func (s *GCPServer) validateObjectLinks(ctx context.Context, links []entities.Hash) error {
	nonZeroLinks := links[:0]
	for _, p := range links {
		if !p.IsZero() {
			nonZeroLinks = append(nonZeroLinks, p)
		}
	}
	links = nonZeroLinks

	objs, err := s.getHeaders(ctx, links)
	if err != nil {
		return err
	}
	var missing []entities.Hash
	for i, o := range objs {
		if !o.Valid && !links[i].IsZero() {
			missing = append(missing, links[i])
		}
	}
	if len(missing) > 0 {
		return &remote.ErrObjectLinkValidation{MissingLinks: missing}
	}
	return nil
}

// SHA256 implements remote.RemoteProvider.
func (s *GCPServer) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	return nil, errors.New("unimplemented")
}
