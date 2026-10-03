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

package server

import (
	"context"
	"io"

	"github.com/google/abfs/shared/dagsync"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/store/localcache"
)

// dagSyncObjectAccessor only exists so that OpenBlob can be adapted to return a ReadSizeCloser instead
// of just a ReadCloser
type dagSyncObjectAccessor struct {
	cache *localcache.LocalCache
}

func (a *dagSyncObjectAccessor) GetObject(ctx context.Context, key entities.Hash) ([]byte, error) {
	return a.cache.GetObject(ctx, key)
}
func (a *dagSyncObjectAccessor) OpenBlob(ctx context.Context, key entities.Hash) (dagsync.ReadSizeCloser, error) {
	rc, err := a.cache.OpenBlob(ctx, key)
	hdr, hdrErr := a.cache.ObjectHeader(ctx, key)

	return blobReadSizeCloser{rc, hdr.Size, hdrErr}, err
}

func (a *dagSyncObjectAccessor) ObjectSize(ctx context.Context, objectHash entities.Hash) (int64, error) {
	hdr, err := a.cache.ObjectHeader(ctx, objectHash)
	if err != nil {
		return 0, err
	}

	return hdr.Size, nil
}

func (a *dagSyncObjectAccessor) BlobSize(ctx context.Context, objectHash entities.Hash) (int64, error) {
	return a.ObjectSize(ctx, objectHash)
}

type blobReadSizeCloser struct {
	rc      io.ReadCloser
	size    int64
	sizeErr error
}

func (b blobReadSizeCloser) Close() error {
	return b.rc.Close()
}

func (b blobReadSizeCloser) Read(p []byte) (int, error) {
	return b.rc.Read(p)
}
func (b blobReadSizeCloser) Size() (int64, error) {
	return b.size, b.sizeErr
}
