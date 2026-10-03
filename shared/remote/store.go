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

package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"

	"github.com/google/abfs/cas/cas"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

type NopCloser struct {
	RemoteProvider
}

func (c NopCloser) Close() error {
	return nil
}

type RemoteStore struct {
	remote RemoteCloser
}

var _ entities.CASStore = (*RemoteStore)(nil)

// NewRemoteStore returns a RemoteStore that does not alter compression of objects.
// If the objects are compressed and the provided RemoteCloser does not handle the compression,
// wrap the RemoteCloser in a CompressiongCloser first.
func NewRemoteStore(remote RemoteCloser) *RemoteStore {
	return &RemoteStore{
		remote: remote,
	}
}
func (s *RemoteStore) CAS() cas.Store {
	return newCASRemoteStore(s)
}

// OpenBlob implements entities.CASStore.
func (s *RemoteStore) OpenBlob(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	reader, err := s.remote.GetObject(ctx, key)
	if err != nil {
		return nil, err
	}

	if _, err := objects.ReadBlobHeader(reader); err != nil {
		if err := reader.Close(); err != nil {
			log.Printf("Failed to close reader: %s", err)
		}
		return nil, err
	}
	return reader, nil
}

// WriteBlob implements entities.CASStore.
func (s *RemoteStore) WriteBlob(ctx context.Context, key entities.Hash, size int64, reader io.Reader) error {
	return s.remote.PutObject(ctx, key, NewBlobReader(io.NopCloser(reader), size))
}

// OpenObjectFile implements entities.CASStore.
func (s *RemoteStore) OpenObjectFile(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	return s.remote.GetObject(ctx, key)
}

// GetObject implements entities.CASStore.
func (s *RemoteStore) GetObject(ctx context.Context, key entities.Hash) ([]byte, error) {
	return FetchBlob(ctx, s.remote, key)
}

func (s *RemoteStore) genTreeMeta(ctx context.Context, content []byte) (entities.TreeMeta, error) {
	entries, err := entities.DecodeDirPlain(content)
	if err != nil {
		return entities.TreeMeta{}, err
	}

	ids := make([]int, 0, len(entries))
	blobHashes := make([]entities.Hash, 0, len(entries))

	for i, ent := range entries {
		if ent.Mode.IsFileOrSymlink() {
			blobHashes = append(blobHashes, ent.Hash)
			ids = append(ids, i)
		}
	}
	sizes := make([]int64, len(entries))
	if len(blobHashes) > 0 {
		hdrs, err := s.remote.Headers(ctx, blobHashes)
		if err != nil {
			return entities.TreeMeta{}, err
		}
		for i, hdr := range hdrs {
			if hdr.IsEmpty() {
				return entities.TreeMeta{}, fmt.Errorf("server does not know about %s", blobHashes[i])
			}
			sizes[ids[i]] = hdr.Size
		}
	}
	return entities.TreeMeta{Sizes: sizes}, nil
}

// GetTree implements entities.CASStore.
func (s *RemoteStore) GetTree(ctx context.Context, key entities.Hash) ([]byte, entities.TreeMeta, error) {
	buf, err := s.GetObject(ctx, key)
	if err != nil {
		return nil, entities.TreeMeta{}, err
	}
	meta, err := s.genTreeMeta(ctx, buf)
	return buf, meta, err
}

// PutObject implements entities.CASStore.
func (s *RemoteStore) PutObject(ctx context.Context, key entities.Hash, content []byte) error {
	return s.WriteObject(ctx, key, bytes.NewReader(content))
}

// PutTree implements entities.CASStore.
func (s *RemoteStore) PutTree(ctx context.Context, key entities.Hash, content []byte, meta entities.TreeMeta) error {
	return s.WriteObject(ctx, key, bytes.NewReader(content))
}

// ObjectSize implements entities.CASStore.
func (s *RemoteStore) ObjectHeader(ctx context.Context, key entities.Hash) (entities.ObjectHeader, error) {
	headers, err := s.remote.Headers(ctx, []entities.Hash{key})
	if err != nil {
		return entities.ObjectHeader{}, nil
	}
	if headers[0].IsEmpty() {
		return entities.ObjectHeader{}, entities.NotExistErrf("cannot find header for %s", key)
	}
	return headers[0], nil
}

// Stat implements entities.CASStore.
func (s *RemoteStore) Stat(ctx context.Context, key entities.Hash) (entities.StatInfo, error) {
	return entities.StatInfo{}, fmt.Errorf("not implemented")
}

// WriteObject implements entities.CASStore.
func (s *RemoteStore) WriteObject(ctx context.Context, key entities.Hash, reader io.Reader) error {
	return s.remote.PutObject(ctx, key, NewContentReader(io.NopCloser(reader), EncodingRaw))
}

// MapResults implements entities.CASStore.
func (s *RemoteStore) MapResults(ctx context.Context, id entities.Hash) ([]entities.Hash, error) {
	return s.remote.MapResults(ctx, id)
}

// RefreshMapResults implements entities.CASStore.
func (s *RemoteStore) RefreshMapResults(ctx context.Context, id entities.Hash) error {
	return nil
}

// AddMapResult implements entities.CASStore.
func (s *RemoteStore) AddMapResult(ctx context.Context, from entities.Hash, to entities.Hash) error {
	return s.remote.AddMapResult(ctx, from, to)
}

// GetRefs implements entities.CASStore.
func (s *RemoteStore) GetRefs(server string, project string, refs []string) ([]entities.Hash, error) {
	return s.remote.GetRefs(context.TODO(), server, project, refs)
}

// SearchRefs implements entities.CASStore.
func (s *RemoteStore) SearchRefs(server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return s.remote.SearchRefs(context.TODO(), server, project, refPrefix, limit)
}

// PutRefs implements entities.CASStore.
func (s *RemoteStore) PutRefs(server string, project string, refs []entities.RefHash) error {
	return s.remote.PutRefs(context.TODO(), server, project, refs)
}

// Close implements entities.CASStore.
func (s *RemoteStore) Close() error {
	return s.remote.Close()
}

// KeepAlive implements entities.CASStore
func (s *RemoteStore) KeepAlive(ctx context.Context, key entities.Hash) error {
	return nil
}

// IsObjectRemote implements entities.CASStore
func (s *RemoteStore) IsObjectRemote(ctx context.Context, key entities.Hash) (bool, error) {
	return false, nil
}

// StartGC implements entities.CASStore
func (s *RemoteStore) StartGC(ctx context.Context) error {
	return nil
}

func (s *RemoteStore) HealthCheck() error {
	return nil
}
