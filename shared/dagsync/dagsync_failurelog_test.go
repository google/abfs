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
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/dagsync"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

// failingUpdater fails the first failures object pushes.
type failingUpdater struct {
	*inmemory.InMemoryServer
	failures atomic.Int32
}

func (u *failingUpdater) PutObject(ctx context.Context, key entities.Hash, reader remote.ContentReadCloser) error {
	if u.failures.Add(-1) >= 0 {
		_ = reader.Close()
		return errors.New("stream reset")
	}
	return u.InMemoryServer.PutObject(ctx, key, reader)
}

type syncBuilder struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *syncBuilder) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *syncBuilder) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

func failedPushLines(logs string) []string {
	var failed []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.HasPrefix(line, "Failed to push") {
			failed = append(failed, line)
		}
	}
	return failed
}

func TestDAGSync_LogsFailedPush(t *testing.T) {
	dagsync.ResetFailureLog(t)
	updater := &failingUpdater{InMemoryServer: inmemory.New()}
	updater.failures.Store(3)
	accessor := newTestAccessor()
	accessor.blobs[hash1] = content1
	logs := new(syncBuilder)
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1, Logger: log.New(logs, "", 0)})
	d.FailCooldown = time.Microsecond

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	d.PutBlob(hash1)
	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	waitForObjects(t, updater.InMemoryServer, 1, 5*time.Second)
	require.NoError(t, d.Wait(10*time.Second))

	// Three failures log attempts 1 and 2 only.
	failed := failedPushLines(logs.String())
	require.Len(t, failed, 2, logs.String())
	require.Contains(t, failed[0], hash1.String())
	require.Contains(t, failed[0], "(attempt 1)")
	require.Contains(t, failed[0], "stream reset")
	require.Contains(t, failed[1], "(attempt 2)")
}

// In a server outage every queued push fails; the process logs at most the limit per minute.
func TestDAGSync_FailedPushLogIsLimited(t *testing.T) {
	dagsync.ResetFailureLog(t)
	const blobs = 50
	updater := &failingUpdater{InMemoryServer: inmemory.New()}
	updater.failures.Store(blobs)
	accessor := newTestAccessor()
	logs := new(syncBuilder)
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 4, Logger: log.New(logs, "", 0)})
	d.FailCooldown = time.Microsecond
	for i := range blobs {
		content := []byte(fmt.Sprintf("content %d", i))
		h := objects.HashBlobBytes(content)
		accessor.blobs[h] = content
		d.PutBlob(h)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	waitForObjects(t, updater.InMemoryServer, blobs, 10*time.Second)
	require.NoError(t, d.Wait(10*time.Second))
	require.Len(t, failedPushLines(logs.String()), 20, logs.String())
}
