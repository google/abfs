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

package dagsync_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/dagsync"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

func waitForObjects(t *testing.T, server *inmemory.InMemoryServer, n int, timeout time.Duration) {
	start := time.Now()
	stat := server.Stats()
	for int(stat.Count())-inmemory.InitObjectsCount < n {
		if time.Since(start) > timeout {
			t.Fatalf("waited %s but got %d objects, expected %d", timeout, stat.Count(), n)
		}
		time.Sleep(time.Millisecond)
		stat = server.Stats()
	}
	require.Equal(t, n, int(stat.Count())-inmemory.InitObjectsCount)
}

type TestAccessor struct {
	sync.Mutex
	blobs       map[entities.Hash][]byte
	objects     map[entities.Hash][]byte
	getObjDelay time.Duration
}

func newTestAccessor() *TestAccessor {
	return &TestAccessor{
		blobs:   make(map[entities.Hash][]byte),
		objects: make(map[entities.Hash][]byte),
	}
}

func (t *TestAccessor) GetObject(ctx context.Context, key entities.Hash) ([]byte, error) {
	t.Lock()
	defer t.Unlock()

	fmt.Println("accessing object", key)
	time.Sleep(t.getObjDelay)
	buf, exists := t.objects[key]
	if !exists {
		return nil, entities.ErrNotExist
	}
	return buf, nil
}
func (t *TestAccessor) OpenBlob(ctx context.Context, key entities.Hash) (dagsync.ReadSizeCloser, error) {
	t.Lock()
	defer t.Unlock()

	fmt.Println("accessing blob", key)

	buf, exists := t.blobs[key]
	if !exists {
		return nil, entities.ErrNotExist
	}
	return &blobReadSizeCloser{rc: io.NopCloser(bytes.NewBuffer(buf)), size: int64(len(buf))}, nil
}

type blobReadSizeCloser struct {
	rc   io.ReadCloser
	size int64
}

func (b blobReadSizeCloser) Close() error {
	return b.rc.Close()
}

func (b blobReadSizeCloser) Read(p []byte) (int, error) {
	return b.rc.Read(p)
}
func (b blobReadSizeCloser) Size() (int64, error) {
	return b.size, nil
}

var _ dagsync.ObjectAccessor = (*TestAccessor)(nil)
