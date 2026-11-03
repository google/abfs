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
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"

	"github.com/google/abfs/shared/remote"
)

func NewInlineDepot(name string, maxBytes int64, errIfTooBig bool) Depot {
	return &InlineDepot{
		name:        name,
		maxBytes:    maxBytes,
		errIfTooBig: errIfTooBig,
	}
}

type InlineDepot struct {
	name        string
	maxBytes    int64
	errIfTooBig bool
}

func (d InlineDepot) Name() string {
	return d.name
}

func (d *InlineDepot) Store(ctx context.Context, name string, r io.Reader) (s Chunk, err error) {
	buf := inlineStorePool.Get().([]byte)
	escaped := false
	defer func() {
		if !escaped {
			inlineStorePool.Put(buf)
		}
	}()

	w := &sliceWriter{buf: buf}
	n, err := copyNBuffer(w, r, d.maxBytes)
	if err != nil && err != io.EOF {
		return s, err
	}
	if d.errIfTooBig && err == nil && n == d.maxBytes {
		escaped = true
		return s, &errTooBig{alreadyRead: &pooledBufferReader{buf: buf[:n], pool: &inlineStorePool}}
	}
	if n < d.maxBytes {
		err = io.EOF
	}
	return Chunk{
		Size:  n,
		Depot: d.name,
		ID:    base64.StdEncoding.EncodeToString(w.Bytes()),
	}, err
}

func (d *InlineDepot) Load(ctx context.Context, s Chunk) (io.ReadCloser, error) {
	content, err := base64.StdEncoding.DecodeString(s.ID)
	if err != nil {
		return nil, err
	}

	if len(content) != int(s.Size) {
		return nil, fmt.Errorf("read size (%d) is different from expected size(%d)", len(content), s.Size)
	}
	return io.NopCloser(bytes.NewBuffer(content)), nil
}

func (d *InlineDepot) Remove(ctx context.Context, s Chunk) error {
	return nil
}

func (d *InlineDepot) LinkedContent(ctx context.Context, s Chunk) (remote.ContentLink, error) {
	return remote.ContentLink{
		LinkType: remote.LinkTypeBase64,
		Size:     uint64(s.Size),
		Content:  s.ID,
	}, nil
}
