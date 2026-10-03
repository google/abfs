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

package filebased

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/pkg/xattr"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers"
	"github.com/google/abfs/shared/store/localcache"
)

type Hash = entities.Hash

const (
	mapLocksCount           = 4096
	defaultMaxCachedHeaders = 20 * 1000 * 1000
	defaultMaxCachedMaps    = 20 * 1000 * 1000
	maxConcurrentIO         = 5000
)

type fileBasedServer struct {
	localDir      string
	validateLinks bool
	validateHash  bool
	objectStore   *localcache.LocalStore
	mapStore      *localcache.MapStore
	refStore      *localcache.LogRefStore
	mapLocks      []sync.Mutex
	headers       []*lru.Cache[Hash, objectMeta]
	maps          []*lru.Cache[Hash, []Hash]
	marker        *Marker
	ioSem         chan struct{}
}

var _ remote.RemoteProvider = (*fileBasedServer)(nil)

type levelsOpt struct {
	value int
}

func LevelsOpt(value int) servers.ServerOpt {
	return levelsOpt{value: value}
}

type maxCachedHeadersOpt struct {
	value int
}

func MaxCachedHeadersOpt(value int) servers.ServerOpt {
	return maxCachedHeadersOpt{value: value}
}

type maxCachedMapsOpt struct {
	value int
}

func MaxCachedMapsOpt(value int) servers.ServerOpt {
	return maxCachedMapsOpt{value: value}
}

func New(dir string, opts ...servers.ServerOpt) (*fileBasedServer, error) {
	levels := 2
	validateHash := true
	validateLinks := true
	maxCachedHeaders := defaultMaxCachedHeaders
	maxCachedMaps := defaultMaxCachedMaps

	for _, opt := range opts {
		switch o := opt.(type) {
		case servers.NoLinkValidation:
			validateLinks = !o.Value
		case servers.NoHashValidation:
			validateHash = !o.Value
		case maxCachedHeadersOpt:
			maxCachedHeaders = o.value
		case maxCachedMapsOpt:
			maxCachedMaps = o.value
		case levelsOpt:
			levels = o.value
		default:
			return nil, fmt.Errorf("unsupported option %T", o)
		}
	}

	objectStore, err := localcache.NewLocalStore(localcache.LocalStoreOpts{Dir: path.Join(dir, "objects"), Levels: levels, Notify: localcache.Notify{}, CompressMode: localcache.Uncompressed})
	if err != nil {
		return nil, err
	}

	mapStore, err := localcache.NewMapStore(path.Join(dir, "map"), levels)
	if err != nil {
		return nil, err
	}

	refStore, err := localcache.NewLogRefStore(path.Join(dir, "refs"))
	if err != nil {
		return nil, err
	}

	s := &fileBasedServer{
		localDir:      dir,
		validateLinks: validateLinks,
		validateHash:  validateHash,
		objectStore:   objectStore,
		mapStore:      mapStore,
		refStore:      refStore,
		mapLocks:      make([]sync.Mutex, mapLocksCount),
		headers:       make([]*lru.Cache[Hash, objectMeta], 256),
		maps:          make([]*lru.Cache[Hash, []Hash], 256),
		ioSem:         make(chan struct{}, maxConcurrentIO),
	}
	for i := range s.headers {
		s.headers[i], err = lru.New[Hash, objectMeta](maxCachedHeaders / 256)
		if err != nil {
			return nil, err
		}
	}
	for i := range s.maps {
		s.maps[i], err = lru.New[Hash, []Hash](maxCachedMaps / 256)
		if err != nil {
			return nil, err
		}
	}

	s.marker = NewMarker(s)

	if err := s.PutObject(context.Background(), entities.EmptyFileHash, remote.NewZStdContentReader(entities.EmptyFileContent)); err != nil {
		return nil, fmt.Errorf("failed to put empty file: %v", err)
	}
	if err := s.PutObject(context.Background(), entities.EmptyDirHash, remote.NewZStdContentReader(entities.EmptyDirContent)); err != nil {
		return nil, fmt.Errorf("failed to put empty directory: %v", err)
	}
	s.marker.Run()

	return s, nil
}

type objectMeta struct {
	Type     string
	Size     int64
	Encoding string
}

func (s *fileBasedServer) acquireIOSem() {
	s.ioSem <- struct{}{}
}
func (s *fileBasedServer) releaseIOSem() {
	<-s.ioSem
}

func (s *fileBasedServer) getMeta(hash Hash) (objectMeta, bool) {
	return s.headers[hash[0]].Get(hash)
}

func (s *fileBasedServer) putMeta(hash Hash, meta objectMeta) {
	s.headers[hash[0]].Add(hash, meta)
}

func (s *fileBasedServer) getMaps(hash Hash) ([]Hash, bool) {
	return s.maps[hash[0]].Get(hash)
}

func (s *fileBasedServer) putMaps(hash Hash, result []Hash) {
	s.maps[hash[0]].Add(hash, result)
}

func (s *fileBasedServer) Close() error {
	return s.refStore.Close()
}

// Version implements remote.RemoteProvider.
func (s *fileBasedServer) Version(ctx context.Context) (string, error) {
	return servers.ServerVersion, nil
}

// WhoAmI implements remote.RemoteProvider.
func (s *fileBasedServer) WhoAmI(ctx context.Context) (string, error) {
	return "", errors.New("unimplemented")
}

func (s *fileBasedServer) header(key remote.Hash) (entities.ObjectHeader, string, error) {
	p := s.objectStore.Path(key)

	s.acquireIOSem()
	headerBytes, err := xattr.LGet(p, "user.header")
	s.releaseIOSem()
	if err != nil {
		return entities.ObjectHeader{}, "", err
	}
	headerStr := string(headerBytes)

	attrParts := strings.Split(headerStr, ":")
	if len(attrParts) != 3 {
		return entities.ObjectHeader{}, "", fmt.Errorf("invalid header attribute %q", headerStr)
	}
	t, sizeStr, encoding := attrParts[0], attrParts[1], attrParts[2]

	n, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return entities.ObjectHeader{}, "", fmt.Errorf("invalid header attribute %q", headerStr)
	}

	s.putMeta(key, objectMeta{
		Type:     t,
		Size:     n,
		Encoding: encoding,
	})

	return entities.ObjectHeader{
		Type: entities.ObjectType(t),
		Size: n,
	}, encoding, nil
}

// Headers implements remote.RemoteProvider.
func (s *fileBasedServer) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	headers := make([]entities.ObjectHeader, len(keys))

	var group errgroup.Group
	for i, key := range keys {
		if meta, exists := s.getMeta(key); exists {
			headers[i] = entities.ObjectHeader{
				Type: entities.ObjectType(meta.Type),
				Size: meta.Size,
			}
			continue
		}
		group.Go(func() error {
			header, _, err := s.header(key)
			if err != nil && !entities.IsNotExist(err) {
				return err
			}
			headers[i] = header
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}

	return headers, nil
}

func (s *fileBasedServer) encoding(key remote.Hash) (string, error) {
	if meta, exists := s.getMeta(key); exists {
		return meta.Encoding, nil
	}
	_, enc, err := s.header(key)
	return enc, err
}

// SHA256s implements RemoteProvider.
func (s *fileBasedServer) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	return nil, errors.New("not implemented")
}

// GetObject implements remote.RemoteProvider.
func (s *fileBasedServer) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	enc, err := s.encoding(key)
	if err != nil {
		return nil, err
	}

	s.acquireIOSem()
	defer s.releaseIOSem()

	reader, err := s.objectStore.OpenFile(ctx, key)
	if err != nil {
		return nil, err
	}

	return remote.NewContentReader(reader, enc), nil

}

// PutObject implements remote.RemoteProvider.
func (s *fileBasedServer) PutObject(ctx context.Context, key remote.Hash, r remote.ContentReadCloser) (retErr error) {
	defer entities.CloseErrCheck(r, &retErr)

	if ok, _ := s.objExists(key); ok {
		return nil
	}

	validator, err := remote.NewValidator(key, r.Encoding(), remote.ExtractLinksOpt(s.validateLinks), remote.ValidateHashOpt(s.validateHash))
	if err != nil {
		return err
	}

	origReader := io.TeeReader(r, validator)

	s.acquireIOSem()
	defer s.releaseIOSem()

	return s.objectStore.Write(ctx, key, origReader, func(fname string) error {
		if err := validator.Close(); err != nil {
			return err
		}

		t := validator.Type()
		n := validator.Size()

		if s.validateLinks {
			if err := s.validateObjectLinks(validator.Links()); err != nil {
				return fmt.Errorf("failed to verify links of %s: %w", key, err)
			}
		}

		if err := xattr.LSet(fname, "user.header", fmt.Appendf(nil, "%s:%d:%s", t, n, r.Encoding())); err != nil {
			return err
		}

		s.putMeta(key, objectMeta{
			Type:     t,
			Size:     n,
			Encoding: r.Encoding(),
		})

		return nil
	})
}

// AddMapResult implements remote.RemoteProvider
func (s *fileBasedServer) AddMapResult(ctx context.Context, from Hash, to Hash) error {
	var missing []Hash
	if s.validateLinks {
		for hashName, hash := range map[string]Hash{"from": from, "to": to} {
			ok, err := s.objExists(hash)
			if err != nil {
				return fmt.Errorf("error finding %s hash %q when validating map links, err: %v", hashName, hash, err)
			}
			if !ok {
				// clients can currently map anything, so we don't know the type of the objects
				missing = append(missing, hash)
			}
		}
	}
	if len(missing) > 0 {
		return &remote.ErrObjectLinkValidation{MissingLinks: missing}
	}

	i := shardHash(from, mapLocksCount)
	s.mapLocks[i].Lock()
	defer s.mapLocks[i].Unlock()

	result, exists := s.getMaps(from)
	if !exists {
		var err error
		s.acquireIOSem()
		result, err = s.mapStore.MapResults(from)
		s.releaseIOSem()
		if err != nil {
			return err
		}

		s.putMaps(from, result)
	}

	s.acquireIOSem()
	err := s.mapStore.AddMapResult(from, to)
	s.releaseIOSem()
	if err != nil {
		return err
	}

	result = append(result, to)
	s.putMaps(from, result)

	return nil
}

// MapResults implements remote.RemoteProvider
func (s *fileBasedServer) MapResults(ctx context.Context, hash Hash) ([]Hash, error) {
	i := shardHash(hash, mapLocksCount)
	s.mapLocks[i].Lock()
	defer s.mapLocks[i].Unlock()

	if result, exists := s.getMaps(hash); exists {
		return result, nil
	}

	s.acquireIOSem()
	result, err := s.mapStore.MapResults(hash)
	s.releaseIOSem()
	if err != nil {
		return nil, err
	}

	s.putMaps(hash, result)

	return result, nil
}

// GetRefs implements remote.RemoteProvider.
func (s *fileBasedServer) GetRefs(ctx context.Context, server string, project string, refs []string) ([]Hash, error) {
	s.acquireIOSem()
	defer s.releaseIOSem()

	return s.refStore.Get(server, project, refs)
}

// PutRefs implements remote.RemoteProvider.
func (s *fileBasedServer) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	var missing []Hash
	if s.validateLinks {
		for _, ref := range refs {
			// we use zero hash as a tombstone or soft delete
			if ref.Hash == entities.ZeroHash {
				continue
			}
			ok, err := s.objExists(ref.Hash)
			if err != nil {
				return fmt.Errorf("error finding hash %q when validating ref %s/%s/%s links, err: %v", ref.Hash, server, project, ref.Ref, err)
			}
			if !ok {
				// clients can currently point refs at anything, so we don't know the type of the destination object
				missing = append(missing, ref.Hash)
			}
		}
	}
	if len(missing) > 0 {
		return &remote.ErrObjectLinkValidation{MissingLinks: missing}
	}

	s.acquireIOSem()
	defer s.releaseIOSem()

	return s.refStore.Put(server, project, refs)
}

// SearchRefs implements remote.RemoteProvider.
func (s *fileBasedServer) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	s.acquireIOSem()
	defer s.releaseIOSem()

	result, err := s.refStore.Search(server, project, refPrefix)
	if err != nil {
		return nil, err
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Ref > result[j].Ref
	})

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}

	return result, nil
}

func (s *fileBasedServer) objExists(hash Hash) (bool, error) {
	if _, exists := s.getMeta(hash); exists {
		return true, nil
	}

	s.acquireIOSem()
	defer s.releaseIOSem()

	return fileExists(s.objectStore.Path(hash))
}

func (s *fileBasedServer) validateObjectLinks(links []Hash) error {
	var missing []Hash

	for _, link := range links {
		ok, err := s.objExists(link)
		if err != nil {
			return err
		}
		if !ok {
			missing = append(missing, link)
		}
	}

	if len(missing) > 0 {
		return &remote.ErrObjectLinkValidation{MissingLinks: missing}
	}

	return nil
}

func shardHash(hash Hash, count int) int {
	i := binary.LittleEndian.Uint32([]byte(hash[:]))
	return int(i % uint32(count))
}

func fileExists(fileName string) (bool, error) {
	_, err := os.Stat(fileName)
	if err == nil {
		return true, nil
	}
	if entities.IsNotExist(err) {
		return false, nil
	}

	return false, err
}

func (s *fileBasedServer) Mark(ctx context.Context, hash Hash, key, value string) (int64, error) {
	return s.marker.Mark(ctx, hash, key, value)
}
