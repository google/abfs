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
	"io"
	"time"

	"cloud.google.com/go/storage"

	"github.com/google/abfs/shared/remote"
)

const defaultURLExpiry = 10 * time.Minute

func NewBucketDepot(name string, maxBytes int64, db *storage.Client, bucketName string) Depot {
	return &BucketDepot{
		name:      name,
		maxBytes:  maxBytes,
		db:        db,
		bucket:    db.Bucket(bucketName),
		urlExpiry: defaultURLExpiry,
	}
}

type BucketDepot struct {
	name      string
	maxBytes  int64
	db        *storage.Client
	bucket    *storage.BucketHandle
	urlExpiry time.Duration
}

func (d *BucketDepot) SetURLExpiry(expiry time.Duration) {
	d.urlExpiry = expiry
}

func (d BucketDepot) Name() string {
	return d.name
}

func (d *BucketDepot) Store(ctx context.Context, name string, r io.Reader) (s Chunk, err error) {
	w := d.bucket.Object(name).NewWriter(ctx)
	if d.maxBytes < int64(w.ChunkSize) {
		w.ChunkSize = int(d.maxBytes)
	}
	n, err := copyNBuffer(w, r, d.maxBytes)
	if err != nil && err != io.EOF {
		w.Close()
		return s, err
	}
	if err := w.Close(); err != nil {
		return s, err
	}
	s.Depot = d.name
	s.ID = name
	s.Size = n
	if n < d.maxBytes {
		err = io.EOF
	}
	return
}

func (d *BucketDepot) Load(ctx context.Context, s Chunk) (io.ReadCloser, error) {
	return d.bucket.Object(s.ID).NewReader(ctx)

}

func (d *BucketDepot) Remove(ctx context.Context, s Chunk) error {
	return d.bucket.Object(s.ID).Delete(ctx)
}

func (d *BucketDepot) LinkedContent(ctx context.Context, s Chunk) (remote.ContentLink, error) {
	expiry := d.urlExpiry
	if expiry == 0 {
		expiry = defaultURLExpiry
	}
	url, err := d.bucket.SignedURL(s.ID, &storage.SignedURLOptions{
		Method:  "GET",
		Expires: time.Now().Add(expiry),
	})
	if err != nil {
		return remote.ContentLink{}, err
	}
	return remote.ContentLink{
		LinkType: remote.LinkTypeURL,
		Size:     uint64(s.Size),
		Content:  url,
	}, nil
}
