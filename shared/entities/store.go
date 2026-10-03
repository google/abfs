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

package entities

import (
	"context"
	"io"

	"github.com/google/abfs/cas/cas"
)

type TreeMeta struct {
	Sizes []int64
}

type BlobStore interface {
	OpenBlob(ctx context.Context, key Hash) (io.ReadCloser, error)
	WriteBlob(ctx context.Context, key Hash, size int64, reader io.Reader) error
}

type ObjectStore interface {
	OpenObjectFile(ctx context.Context, key Hash) (io.ReadCloser, error)
	GetObject(ctx context.Context, key Hash) ([]byte, error)
	GetTree(ctx context.Context, key Hash) ([]byte, TreeMeta, error)
	PutObject(ctx context.Context, key Hash, content []byte) error
	PutTree(ctx context.Context, key Hash, content []byte, meta TreeMeta) error
	WriteObject(ctx context.Context, key Hash, reader io.Reader) error
	ObjectHeader(ctx context.Context, key Hash) (ObjectHeader, error)
}

type MapStore interface {
	MapResults(ctx context.Context, id Hash) ([]Hash, error)
	RefreshMapResults(ctx context.Context, id Hash) error
	AddMapResult(ctx context.Context, from, to Hash) error
}

type RefStore interface {
	GetRefs(server string, project string, refs []string) ([]Hash, error)
	SearchRefs(server string, project string, refPrefix string, limit int) ([]ProjectRefHash, error)
	PutRefs(server string, project string, refs []RefHash) error
}

type CASStore interface {
	BlobStore
	ObjectStore
	MapStore
	RefStore
	io.Closer
	CAS() cas.Store
	// TODO: Optimize keep alive: change the interface to keys []entities.Hash
	KeepAlive(ctx context.Context, key Hash) error
	// IsObjectRemote is for both objects and blobs.
	// It will firstly check the remote attribute if the object is in the local cache.
	// If it's not, it will then check if it's on the remote server by requesting the header.
	IsObjectRemote(ctx context.Context, key Hash) (bool, error)
	StartGC(ctx context.Context) error
	HealthCheck() error
}

func FirstError(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func CloseErrCheck(c io.Closer, err *error) {
	closeErr := c.Close()
	if closeErr == nil || *err != nil {
		return
	}
	*err = closeErr
}
