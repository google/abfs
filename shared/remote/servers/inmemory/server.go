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

package inmemory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers"
	"github.com/google/abfs/shared/zstd"
)

type ProjectRefs map[string]remote.Hash

type ServerRefs map[string]ProjectRefs

type InMemoryServer struct {
	lock        sync.RWMutex
	brokenLinks int
	failCount   int
	headers     map[entities.Hash]entities.ObjectHeader
	sha256      map[entities.Hash]entities.SHA256
	objects     map[entities.Hash][]byte
	encodings   map[entities.Hash]string
	maps        map[entities.Hash]map[entities.Hash]struct{}
	refs        map[string]ServerRefs
}

type ObjectReader struct {
	hash     entities.Hash
	buf      *bytes.Reader
	encoding string
}

var _ remote.RemoteProvider = (*InMemoryServer)(nil)

const InitObjectsCount = 2 // empty blob and empty dir

func New(opts ...servers.ServerOpt) *InMemoryServer {
	s := &InMemoryServer{
		headers:   make(map[entities.Hash]entities.ObjectHeader),
		sha256:    make(map[entities.Hash]entities.SHA256),
		objects:   make(map[entities.Hash][]byte),
		maps:      make(map[entities.Hash]map[entities.Hash]struct{}),
		refs:      make(map[string]ServerRefs),
		encodings: make(map[entities.Hash]string),
	}

	if err := s.PutObject(context.Background(), entities.EmptyFileHash, remote.NewZStdContentReader(entities.EmptyFileContent)); err != nil {
		panic(fmt.Errorf("failed to put empty file: %v", err))
	}
	if err := s.PutObject(context.Background(), entities.EmptyDirHash, remote.NewZStdContentReader(entities.EmptyDirContent)); err != nil {
		panic(fmt.Errorf("failed to put empty directory: %v", err))
	}

	return s
}

// Headers implements remote.RemoteProvider.
func (s *InMemoryServer) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	result := make([]entities.ObjectHeader, len(keys))
	for i, hash := range keys {
		result[i] = s.headers[hash]
	}
	return result, nil
}

// SHA256 implements RemoteProvider.
func (s *InMemoryServer) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	shas := make([]entities.SHA256, len(keys))
	for i, key := range keys {
		if sha, ok := s.sha256[key]; ok {
			shas[i] = sha
		} else {
			content, ok := s.objects[key]
			if !ok {
				continue
			}
			content, err := s.decode(content, s.encodings[key])
			if err != nil {
				continue
			}
			_, rest, err := entities.DecodeObject(content)
			if err != nil {
				continue
			}

			// for SHA256 we exclude the object header from the hash
			shas[i] = entities.SHA256(sha256.Sum256(rest))
		}
	}
	return shas, nil
}

// GetObject implements remote.RemoteProvider.
func (s *InMemoryServer) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	s.lock.RLock()
	content, ok := s.objects[key]
	encoding := s.encodings[key]
	s.lock.RUnlock()

	if !ok {
		return nil, entities.NotExistErrf("cannot find object %s", key)
	}

	return &ObjectReader{
		hash:     key,
		encoding: encoding,
		buf:      bytes.NewReader(content),
	}, nil
}

func (s *InMemoryServer) decode(content []byte, decoding string) ([]byte, error) {
	switch decoding {
	case "", remote.EncodingRaw:
		return content, nil
	case remote.EncodingZStd:
		return zstd.Decompress(content)
	default:
		return nil, fmt.Errorf("unsupported decoding %q", decoding)
	}
}

// PutObject implements remote.RemoteProvider.
func (s *InMemoryServer) PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) (retErr error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	defer entities.CloseErrCheck(reader, &retErr)

	if _, ok := s.objects[key]; ok {
		return nil
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		return err
	}

	decoded, err := s.decode(content, reader.Encoding())
	if err != nil {
		return err
	}

	t, rest, err := entities.DecodeObject(decoded)
	if err != nil {
		return fmt.Errorf("failed to decode %s: %w", key, err)
	}
	header := entities.ObjectHeader{
		Type: entities.ObjectType(t),
		Size: int64(len(rest)),
	}
	// for SHA256 we exclude the object header from the hash
	s.sha256[key] = entities.SHA256(sha256.Sum256(rest))
	if t != "blob" {
		if err := s.ensureLinksExist(key, decoded); err != nil {
			return err
		}
	}
	log.Println("put object", key, header.Type, header.Size)
	s.objects[key] = content
	s.headers[key] = header
	s.encodings[key] = reader.Encoding()
	return nil
}

func (s *InMemoryServer) ensureLinksExist(key remote.Hash, content []byte) error {
	links, err := objects.Factory.Links(key, content)
	if err != nil {
		return fmt.Errorf("failed to decode %s: %w", key, err)
	}
	var misses []entities.Hash
	for _, link := range links {
		if link == entities.ZeroHash {
			continue
		}
		if _, ok := s.objects[link]; !ok {
			misses = append(misses, link)
		}
	}
	if len(misses) > 0 {
		s.brokenLinks++
		return &remote.ErrObjectLinkValidation{
			MissingLinks: misses,
		}
	}
	return nil
}

// MapResults implements remote.RemoteProvider.
func (s *InMemoryServer) MapResults(ctx context.Context, hash remote.Hash) ([]remote.Hash, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	m := s.maps[hash]
	results := make([]remote.Hash, 0, len(m))
	for k := range m {
		results = append(results, k)
	}
	return results, nil
}

// AddMapResult implements remote.RemoteProvider.
func (s *InMemoryServer) AddMapResult(ctx context.Context, from remote.Hash, to remote.Hash) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	var misses []entities.Hash
	for _, link := range []entities.Hash{from, to} {
		if _, ok := s.objects[link]; !ok {
			misses = append(misses, link)
		}
	}
	if len(misses) > 0 {
		s.brokenLinks++
		return &remote.ErrObjectLinkValidation{
			MissingLinks: misses,
		}
	}

	log.Printf("add map from %s to %s", from, to)

	if s.maps[from] == nil {
		s.maps[from] = make(map[entities.Hash]struct{})
	}
	s.maps[from][to] = struct{}{}

	return nil
}

// GetRefs implements remote.RemoteProvider.
func (s *InMemoryServer) GetRefs(ctx context.Context, server string, project string, refs []string) ([]remote.Hash, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	prj := s.getProjectRefs(server, project, false)

	results := make([]remote.Hash, 0, len(refs))
	for _, ref := range refs {
		hash := prj[ref] // zero value is fine
		results = append(results, hash)
	}

	return results, nil
}

// PutRefs implements remote.RemoteProvider.
func (s *InMemoryServer) PutRefs(ctx context.Context, server string, project string, refs []remote.RefHash) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	prj := s.getProjectRefs(server, project, true)

	misses := make([]entities.Hash, 0)
	for _, ref := range refs {
		if ref.Hash == entities.ZeroHash {
			continue
		}
		if _, ok := s.objects[ref.Hash]; !ok {
			misses = append(misses, ref.Hash)
		}
	}
	if len(misses) > 0 {
		s.brokenLinks++
		return &remote.ErrObjectLinkValidation{
			MissingLinks: misses,
		}
	}

	log.Printf("put %d refs for server=%s project=%s", len(refs), server, project)
	for _, ref := range refs {
		prj[ref.Ref] = ref.Hash
	}

	return nil
}

// SearchRefs implements remote.RemoteProvider.
func (s *InMemoryServer) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]remote.ProjectRefHash, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	log.Println("search refs", server, project, refPrefix)

	result := make([]remote.ProjectRefHash, 0)
	if project != "" {
		prjRefs := s.getProjectRefs(server, project, false)
		searchRefs(project, prjRefs, refPrefix, &result)
	} else {
		serverRefs := s.getServerRefs(server, false)
		for prj, prjRefs := range serverRefs {
			searchRefs(prj, prjRefs, refPrefix, &result)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Ref > result[j].Ref
	})

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}

	return result, nil
}

func searchRefs(project string, prjRefs ProjectRefs, refPrefix string, projectRefHash *[]remote.ProjectRefHash) {
	refPrefix = strings.Trim(refPrefix, "/")
	refPrefixDir := refPrefix + "/"

	for key, hash := range prjRefs {
		if refPrefix == key || strings.HasPrefix(key, refPrefixDir) {
			*projectRefHash = append(*projectRefHash, remote.ProjectRefHash{Project: project, Ref: key, Hash: hash})
		}
	}
}

func (s *InMemoryServer) getServerRefs(server string, addIfMissing bool) ServerRefs {
	if s.refs[server] == nil {
		if !addIfMissing {
			return nil
		}
		s.refs[server] = make(ServerRefs)
	}
	return s.refs[server]
}

func (s *InMemoryServer) getProjectRefs(server, project string, addIfMissing bool) ProjectRefs {
	serverRefs := s.getServerRefs(server, addIfMissing)

	if serverRefs[project] == nil {
		if !addIfMissing {
			return nil
		}
		serverRefs[project] = make(ProjectRefs)
	}
	return serverRefs[project]
}

// Mark implements remote.RemoteProvider.
func (i *InMemoryServer) Mark(ctx context.Context, hash remote.Hash, key string, value string) (int64, error) {
	return 0, fmt.Errorf("unimplemented")
}

// Version implements remote.RemoteProvider.
func (s *InMemoryServer) Version(ctx context.Context) (string, error) {
	return servers.ServerVersion, nil
}

// WhoAmI implements remote.RemoteProvider.
func (s *InMemoryServer) WhoAmI(ctx context.Context) (string, error) {
	return "", errors.New("unimplemented")
}

type Stats struct {
	ObjectsCount int64
	MapsCount    int64
	RefsCount    int64
	BrokenLinks  int64
	FailCount    int64
}

func (s *Stats) Count() int64 {
	return s.ObjectsCount + s.MapsCount + s.RefsCount
}

func (s *InMemoryServer) Stats() Stats {
	s.lock.RLock()
	defer s.lock.RUnlock()

	stat := Stats{
		ObjectsCount: int64(len(s.objects)),
		BrokenLinks:  int64(s.brokenLinks),
		FailCount:    int64(s.failCount),
	}
	for from := range s.maps {
		stat.MapsCount += int64(len(s.maps[from]))
	}

	for _, server := range s.refs {
		for _, project := range server {
			stat.RefsCount += int64(len(project))
		}
	}
	return stat
}

func (s *InMemoryServer) Close() error {
	return nil
}

// Close implements remote.ObjectReadCloserI.
func (o *ObjectReader) Close() error {
	return nil
}

// Encoding implements remote.ObjectReadCloserI.
func (o *ObjectReader) Encoding() string {
	return o.encoding
}

// Hash implements remote.ObjectReadCloserI.
func (o *ObjectReader) Hash() remote.Hash {
	return o.hash
}

// Read implements remote.ObjectReadCloserI.
func (o *ObjectReader) Read(p []byte) (n int, err error) {
	return o.buf.Read(p)
}
