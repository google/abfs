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
	"strconv"

	"cloud.google.com/go/spanner"

	"github.com/google/abfs/shared/remote"
)

func NewSpannerDepot(name string, maxBytes int64, db *spanner.Client, tableName string) Depot {
	return &SpannerDepot{
		name:      name,
		maxBytes:  maxBytes,
		db:        db,
		tableName: tableName,
	}
}

type SpannerDepot struct {
	name      string
	maxBytes  int64
	db        *spanner.Client
	tableName string
}

func (d SpannerDepot) Name() string {
	return d.name
}

func (d *SpannerDepot) Store(ctx context.Context, name string, r io.Reader) (s Chunk, err error) {
	buf := chunkStorePool.Get().([]byte)
	defer chunkStorePool.Put(buf)

	w := &sliceWriter{buf: buf}
	n, err := copyNBuffer(w, r, d.maxBytes)
	if err != nil && err != io.EOF {
		return s, err
	}
	stmt := spanner.Statement{
		SQL: fmt.Sprintf(`INSERT INTO %s (AddedOn, Content) VALUES (CURRENT_TIMESTAMP(), @content) THEN RETURN ChunkId`, d.tableName),
		Params: map[string]any{
			"content": w.Bytes(),
		},
	}
	var generatedID int64
	_, err = d.db.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		iter := txn.Query(ctx, stmt)
		defer iter.Stop()

		row, err := iter.Next()
		if err != nil {
			return err
		}
		if err := row.Columns(&generatedID); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return s, err
	}
	s.Depot = d.name
	s.ID = fmt.Sprintf("%d", generatedID)
	s.Size = n
	if n < d.maxBytes {
		err = io.EOF
	}
	return s, err
}

func (d *SpannerDepot) Load(ctx context.Context, s Chunk) (io.ReadCloser, error) {
	content, err := d.load(ctx, s)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewBuffer(content)), nil
}

func (d *SpannerDepot) load(ctx context.Context, s Chunk) ([]byte, error) {
	chunkID, err := strconv.ParseInt(s.ID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("spanner chunk-id (%q) is not integer: %w", s.ID, err)
	}

	stmt := spanner.Statement{
		SQL: fmt.Sprintf(`SELECT Content FROM %s WHERE ChunkID = @chunkId`, d.tableName),
		Params: map[string]interface{}{
			"chunkId": chunkID,
		},
	}

	var content []byte
	err = d.db.Single().Query(ctx, stmt).Do(func(row *spanner.Row) error {
		return row.Columns(&content)
	})
	if err != nil {
		return nil, err
	}

	if len(content) != int(s.Size) {
		return nil, fmt.Errorf("read size (%d) is different from expected size(%d)", len(content), s.Size)
	}

	return content, nil
}

func (d *SpannerDepot) Remove(ctx context.Context, s Chunk) error {
	chunkID, err := strconv.ParseInt(s.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("spanner chunk-id (%q) is not integer: %w", s.ID, err)
	}

	_, err = d.db.Apply(ctx, []*spanner.Mutation{
		spanner.Delete(d.tableName, spanner.Key{chunkID}),
	})

	return err
}

func (d *SpannerDepot) LinkedContent(ctx context.Context, s Chunk) (remote.ContentLink, error) {
	content, err := d.load(ctx, s)
	if err != nil {
		return remote.ContentLink{}, err
	}

	return remote.ContentLink{
		LinkType: remote.LinkTypeBase64,
		Size:     uint64(len(content)),
		Content:  base64.StdEncoding.EncodeToString(content),
	}, nil
}
