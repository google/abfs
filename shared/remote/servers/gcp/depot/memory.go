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
	"sync"

	"github.com/google/uuid"

	"github.com/google/abfs/shared/remote"
)

func NewMemoryDepot(name string, maxBytes int64) Depot {
	return &MemoryDepot{
		name:     name,
		blobs:    new(sync.Map),
		maxBytes: maxBytes,
	}
}

type MemoryDepot struct {
	name     string
	blobs    *sync.Map
	maxBytes int64
}

func (d MemoryDepot) Name() string {
	return d.name
}

func (d *MemoryDepot) Store(ctx context.Context, name string, r io.Reader) (s Chunk, err error) {
	buf := bytes.NewBuffer(nil)
	n, err := io.CopyN(buf, r, d.maxBytes)
	if err != nil && err != io.EOF {
		return s, err
	}
	s.Depot = d.name
	s.ID = name
	if s.ID == "" {
		s.ID = uuid.New().String()
	}
	s.Size = n
	d.blobs.Store(s.ID, buf.Bytes())
	if n < d.maxBytes {
		err = io.EOF
	}
	return
}

func (d *MemoryDepot) Load(ctx context.Context, s Chunk) (io.ReadCloser, error) {
	buf, ok := d.blobs.Load(s.ID)
	if !ok {
		return nil, fmt.Errorf("memory-chunk %q: %w", s.ID, ErrNotFound)
	}

	return io.NopCloser(bytes.NewBuffer(buf.([]byte))), nil
}

func (d *MemoryDepot) Remove(ctx context.Context, s Chunk) error {
	_, ok := d.blobs.LoadAndDelete(s.ID)
	if !ok {
		return fmt.Errorf("memory-chunk %q: %w", s.ID, ErrNotFound)
	}
	return nil
}

func (d *MemoryDepot) LinkedContent(ctx context.Context, s Chunk) (remote.ContentLink, error) {
	buf, ok := d.blobs.Load(s.ID)
	if !ok {
		return remote.ContentLink{}, fmt.Errorf("memory-chunk %q: %w", s.ID, ErrNotFound)
	}
	content := buf.([]byte)
	return remote.ContentLink{
		LinkType: remote.LinkTypeBase64,
		Size:     uint64(s.Size),
		Content:  base64.StdEncoding.EncodeToString(content),
	}, nil
}

func (d MemoryDepot) count() int {
	count := 0
	d.blobs.Range(func(key, value any) bool {
		count++
		return true
	})
	return count
}
