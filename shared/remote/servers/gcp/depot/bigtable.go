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

	"cloud.google.com/go/bigtable"

	"github.com/google/abfs/shared/remote"
)

type BigtableDepot struct {
	name     string
	maxBytes int64
	btTable  *bigtable.Table
}

func NewBigtableDepot(name string, maxBytes int64, btClient *bigtable.Client, tableName string) Depot {
	return &BigtableDepot{
		name:     name,
		maxBytes: maxBytes,
		btTable:  btClient.Open(tableName),
	}
}

func (d *BigtableDepot) Name() string {
	return d.name
}

func (d *BigtableDepot) Store(ctx context.Context, name string, r io.Reader) (s Chunk, err error) {
	buf := chunkStorePool.Get().([]byte)
	defer chunkStorePool.Put(buf)

	w := &sliceWriter{buf: buf}
	n, err := copyNBuffer(w, r, d.maxBytes)
	if err != nil && err != io.EOF {
		return s, err
	}

	content := w.Bytes()

	mut := bigtable.NewMutation()
	mut.Set("data", "content", bigtable.Now(), content)

	if errApply := d.btTable.Apply(ctx, name, mut); errApply != nil {
		return s, fmt.Errorf("failed to write chunk %s to Bigtable: %w", name, errApply)
	}

	s.Depot = d.name
	s.ID = name
	s.Size = n
	if n < d.maxBytes {
		err = io.EOF
	}
	return s, err
}

func (d *BigtableDepot) Load(ctx context.Context, s Chunk) (io.ReadCloser, error) {
	row, err := d.btTable.ReadRow(ctx, s.ID, bigtable.RowFilter(bigtable.LatestNFilter(1)))
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	var content []byte
	for _, item := range row["data"] {
		if item.Column == "data:content" {
			content = item.Value
			break
		}
	}
	if len(content) == 0 {
		return nil, ErrNotFound
	}
	if int64(len(content)) != s.Size {
		return nil, fmt.Errorf("read size (%d) is different from expected size (%d)", len(content), s.Size)
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (d *BigtableDepot) Remove(ctx context.Context, s Chunk) error {
	mut := bigtable.NewMutation()
	mut.DeleteRow()
	return d.btTable.Apply(ctx, s.ID, mut)
}

func (d *BigtableDepot) LinkedContent(ctx context.Context, s Chunk) (remote.ContentLink, error) {
	r, err := d.Load(ctx, s)
	if err != nil {
		return remote.ContentLink{}, err
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return remote.ContentLink{}, err
	}
	return remote.ContentLink{
		LinkType: remote.LinkTypeBase64,
		Size:     uint64(len(content)),
		Content:  base64.StdEncoding.EncodeToString(content),
	}, nil
}
