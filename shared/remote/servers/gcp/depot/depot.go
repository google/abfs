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

package depot

import (
	"context"
	"errors"
	"io"

	"github.com/google/abfs/shared/remote"
)

var ErrNotFound = errors.New("chunk not found")
var ErrNoLinkedContent = errors.New("depot does not support linked content")

type errTooBig struct {
	alreadyRead io.Reader
}

func (e *errTooBig) Error() string {
	return "input is too big for the chunk"
}

type Chunk struct {
	Size  int64  `json:"size"`
	Depot string `json:"depot"`
	ID    string `json:"id"`
}

type Depot interface {
	Name() string
	Store(ctx context.Context, name string, r io.Reader) (Chunk, error)
	Load(ctx context.Context, s Chunk) (io.ReadCloser, error)
	Remove(ctx context.Context, s Chunk) error
	LinkedContent(ctx context.Context, s Chunk) (remote.ContentLink, error)
}
