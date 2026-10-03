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
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/google/abfs/shared/entities"
)

type (
	Hash           = entities.Hash
	RefHash        = entities.RefHash
	ProjectRefHash = entities.ProjectRefHash
)

type RemoteProvider interface {
	// Headers returns a slice of headers corresponding to the provided keys.
	// The returned slice is ordered to match the input keys. If a header for a
	// given key is not found, the header at the corresponding index will be empty
	// (i.e., its IsEmpty() method will return true).
	Headers(ctx context.Context, keys []Hash) ([]entities.ObjectHeader, error)
	SHA256(ctx context.Context, keys []Hash) ([]entities.SHA256, error)

	GetObject(ctx context.Context, key Hash) (ContentReadCloser, error)
	PutObject(ctx context.Context, key Hash, reader ContentReadCloser) error

	MapResults(ctx context.Context, from Hash) ([]Hash, error)
	AddMapResult(ctx context.Context, from, to Hash) error

	GetRefs(ctx context.Context, server string, project string, refs []string) ([]Hash, error)
	SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]ProjectRefHash, error)
	PutRefs(ctx context.Context, server string, project string, refs []RefHash) error

	Mark(ctx context.Context, hash Hash, key, value string) (int64, error)

	Version(ctx context.Context) (string, error)
	WhoAmI(ctx context.Context) (string, error)
}

type RemoteCloser interface {
	RemoteProvider
	io.Closer
}

type ContentReadCloser interface {
	io.ReadCloser
	Encoding() string
}

// BundledObject represents an additional object to be eagerly persisted.
type BundledObject struct {
	Hash Hash
	Data []byte
}

// BundledMapResult represents an additional map result to be eagerly persisted.
type BundledMapResult struct {
	From Hash
	To   Hash
}

// BundledReadCloser extends ContentReadCloser to allow a GetObject call to return
// additional "side-car" objects discovered during the primary fetch.
// This is used to optimize fetches (e.g., returning symlink targets along with a tree).
type BundledReadCloser interface {
	ContentReadCloser
	// Blobs returns objects that should be stored in the local blob store, i.e. files and symlinks.
	Blobs() []BundledObject
	// Objects returns objects that should be stored in the local object store, i.e. trees.
	Objects() []BundledObject
	// MapResults returns mapping entries that should be stored in the local map store.
	MapResults() []BundledMapResult
}

type ErrObjectLinkValidation struct {
	MissingLinks []Hash
}

func (e *ErrObjectLinkValidation) Error() string {
	str := make([]string, len(e.MissingLinks))
	for i, l := range e.MissingLinks {
		str[i] = l.String()
	}
	return fmt.Sprintf("missing: %s", strings.Join(str, ", "))
}

var _ error = (*ErrObjectLinkValidation)(nil)

type ErrPermissionDenied struct {
	Action string
}

func (e *ErrPermissionDenied) Error() string {
	return fmt.Sprintf("permission denied on %s", e.Action)
}

var _ error = (*ErrPermissionDenied)(nil)
