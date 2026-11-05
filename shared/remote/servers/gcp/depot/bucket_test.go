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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
)

// newEmulatorBucket creates a bucket on the Cloud Storage emulator named by
// STORAGE_EMULATOR_HOST_GRPC and returns a handle and its client.
func newEmulatorBucket(t *testing.T) (*storage.Client, string) {
	t.Helper()
	if os.Getenv("STORAGE_EMULATOR_HOST_GRPC") == "" {
		t.Skip("STORAGE_EMULATOR_HOST_GRPC is not set")
	}
	ctx := context.Background()
	client, err := storage.NewGRPCClient(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	b := make([]byte, 6)
	_, err = rand.Read(b)
	require.NoError(t, err)
	name := "b" + hex.EncodeToString(b)
	require.NoError(t, client.Bucket(name).Create(ctx, "test-proj", nil))
	return client, name
}

func listObjects(t *testing.T, client *storage.Client, bucket string) map[string]int64 {
	t.Helper()
	objs := make(map[string]int64)
	it := client.Bucket(bucket).Objects(context.Background(), nil)
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return objs
		}
		require.NoError(t, err)
		objs[attrs.Name] = attrs.Size
	}
}

func TestBucketDepot_EmptyInputCreatesNoObject(t *testing.T) {
	require := require.New(t)
	client, bucket := newEmulatorBucket(t)
	d := NewBucketDepot("bucket", 8, client, bucket)

	s, err := d.Store(context.Background(), "empty", bytes.NewReader(nil))
	require.ErrorIs(err, io.EOF)
	require.Zero(s.Size)
	require.Empty(listObjects(t, client, bucket))
}

func TestMasterDepot_InputEndingAtChunkLimitCreatesNoEmptyObject(t *testing.T) {
	require := require.New(t)
	client, bucket := newEmulatorBucket(t)
	master := NewMasterDepot([]Depot{
		NewBucketDepot("bucket", 8, client, bucket),
		NewBucketDepot("bucket", 16, client, bucket),
	})

	chunks, err := master.StoreChunks(context.Background(), "exact", strings.NewReader("12345678"))
	require.NoError(err)
	require.Len(chunks, 1)
	require.Equal(map[string]int64{"exact-0": 8}, listObjects(t, client, bucket))
}

type failingReader struct {
	data []byte
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestBucketDepot_ReadErrorCreatesNoObject(t *testing.T) {
	require := require.New(t)
	client, bucket := newEmulatorBucket(t)
	d := NewBucketDepot("bucket", 1<<20, client, bucket)

	readErr := errors.New("broken stream")
	_, err := d.Store(context.Background(), "partial", &failingReader{data: bytes.Repeat([]byte("x"), 1000), err: readErr})
	require.ErrorIs(err, readErr)
	require.Empty(listObjects(t, client, bucket))
}
