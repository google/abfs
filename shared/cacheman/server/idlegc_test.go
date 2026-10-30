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

package server

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers/inmemory"
	"github.com/google/abfs/shared/store/localcache"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestMountTracker(t *testing.T) {
	clock := &testClock{now: time.Unix(1_000_000, 0)}
	m := newMountTracker(clock.Now)
	client := &Connection{}
	mount := &Connection{}

	clock.Advance(30 * time.Second)
	require.Equal(t, 30*time.Second, m.idleFor())

	// Any new connection resets the quiet time: a kernel mount sends its first
	// check_status only after its first poll interval.
	m.add(client)
	clock.Advance(10 * time.Second)
	require.Equal(t, 10*time.Second, m.idleFor())

	m.add(mount)
	m.markKernel(mount)
	clock.Advance(time.Minute)
	require.Equal(t, 1, m.kernelMounts())
	require.Equal(t, time.Duration(0), m.idleFor())

	// Closing a client connection does not end a quiet period.
	m.remove(client)
	require.Equal(t, time.Duration(0), m.idleFor())

	m.remove(mount)
	clock.Advance(20 * time.Second)
	require.Equal(t, 0, m.kernelMounts())
	require.Equal(t, 20*time.Second, m.idleFor())

	// Removing twice is harmless.
	m.remove(mount)
	require.Equal(t, 0, m.kernelMounts())
}

func TestMountTracker_DeleteAllowed(t *testing.T) {
	clock := &testClock{now: time.Unix(1_000_000, 0)}
	m := newMountTracker(clock.Now)
	clock.Advance(idleGCQuietWindow)
	require.True(t, m.deleteAllowed())

	// A blob is written before it is announced, so a delete waits for quiet.
	m.noteNewObject()
	require.False(t, m.deleteAllowed())
	clock.Advance(idleGCNewObjectQuiet)
	require.True(t, m.deleteAllowed())

	mount := &Connection{}
	m.add(mount)
	m.markKernel(mount)
	clock.Advance(idleGCQuietWindow)
	require.False(t, m.deleteAllowed())
	m.remove(mount)
	require.False(t, m.deleteAllowed())
	clock.Advance(idleGCQuietWindow)
	require.True(t, m.deleteAllowed())
}

func TestConnectionCountsKernelMount(t *testing.T) {
	clock := &testClock{now: time.Unix(1_000_000, 0)}
	c := &CacheMan{
		printer: &bufferPrinter{},
		mounts:  newMountTracker(clock.Now),
	}
	c.setHealthStatusCode(0, "")

	conn, serverConn := net.Pipe()
	defer conn.Close()

	srvConn := newConnection(c, serverConn)
	go func() {
		_ = srvConn.Serve()
	}()
	require.Equal(t, 0, c.mounts.kernelMounts())

	require.NoError(t, protocol.WriteCheckStatusReq(conn, 1))
	_, err := protocol.ReadCheckStatusResp(conn)
	require.NoError(t, err)
	require.Equal(t, 1, c.mounts.kernelMounts())

	require.NoError(t, srvConn.Close())
	require.Equal(t, 0, c.mounts.kernelMounts())
}

// blockingPutClient holds every PutObject until release is closed, so dagsync
// keeps a running node.
type blockingPutClient struct {
	ABFSClient
	release chan struct{}
}

func (b *blockingPutClient) PutObject(ctx context.Context, key entities.Hash, reader remote.ContentReadCloser) error {
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.ABFSClient.PutObject(ctx, key, reader)
}

type idleGCTest struct {
	c     *CacheMan
	clock *testClock
}

func newIdleGCTest(t *testing.T, client ABFSClient) *idleGCTest {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")
	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	trueVal := true
	inst := instance.NewInstance("default", tempDir, tempDir, "abfs", &instance.ConfigOptional{
		CacheDir:    &cacheDir,
		LogDir:      &logDir,
		LocalOnly:   &trueVal,
		EnableIndex: &trueVal,
	}, instance.DefaultFlavor)
	t.Cleanup(func() {
		require.NoError(t, inst.Close())
	})

	c, err := New(inst, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, c.Stop())
	})

	if client == nil {
		client = inmemory.New()
	}
	c.config.LocalOnly = false
	c.abfsClient = client
	c.setupDAGSync(context.Background(), c.config, true)
	c.setHealthStatusCode(0, "")

	clock := &testClock{now: time.Now()}
	c.mounts = newMountTracker(clock.Now)
	return &idleGCTest{c: c, clock: clock}
}

func (it *idleGCTest) putBlob(t *testing.T, content string, isRemote bool, lastAlive time.Time) entities.Hash {
	data := []byte(content)
	h := objects.HashBlobBytes(data)
	require.NoError(t, it.c.store.WriteBlob(context.Background(), h, bytes.NewReader(data)))
	_, _, _, _, err := it.c.index.Put(h, &IndexEntry{
		EntryType:     EntryTypeBlob,
		Local:         true,
		Remote:        isRemote,
		Size:          uint64(len(data)),
		LastAliveTime: lastAlive,
	})
	require.NoError(t, err)
	return h
}

func (it *idleGCTest) hasBlob(t *testing.T, h entities.Hash) bool {
	ok, err := it.c.store.HasBlob(context.Background(), h)
	require.NoError(t, err)
	return ok
}

func TestIdleLocalGC(t *testing.T) {
	it := newIdleGCTest(t, nil)
	now := time.Now()
	old := it.putBlob(t, "old local-only output", false, now.Add(2*localGCThreshold))
	young := it.putBlob(t, "young local-only output", false, now)
	oldRemote := it.putBlob(t, "old uploaded output", true, now.Add(2*localGCThreshold))

	it.clock.Advance(idleGCStartWindow)
	ran, err := it.c.runIdleGC(context.Background())
	require.NoError(t, err)
	require.True(t, ran)

	require.Nil(t, it.c.index.Get(old))
	require.False(t, it.hasBlob(t, old))
	require.NotNil(t, it.c.index.Get(young))
	require.True(t, it.hasBlob(t, young))
	require.NotNil(t, it.c.index.Get(oldRemote))
	require.True(t, it.hasBlob(t, oldRemote))
}

func TestIdleLocalGC_Gates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, it *idleGCTest)
	}{
		{
			name: "kernel mount connected",
			setup: func(t *testing.T, it *idleGCTest) {
				mount := &Connection{}
				it.c.mounts.add(mount)
				it.c.mounts.markKernel(mount)
				it.clock.Advance(idleGCStartWindow)
			},
		},
		{
			name:  "mounts still reconnecting after a cacheman start",
			setup: func(t *testing.T, it *idleGCTest) { it.clock.Advance(idleGCStartWindow / 2) },
		},
		{
			name: "connection accepted within the quiet window",
			setup: func(t *testing.T, it *idleGCTest) {
				it.clock.Advance(idleGCStartWindow)
				it.c.mounts.add(&Connection{})
				it.clock.Advance(idleGCQuietWindow / 2)
			},
		},
		{
			name: "remote read-only",
			setup: func(t *testing.T, it *idleGCTest) {
				it.c.config.RemoteReadOnly = true
				it.clock.Advance(idleGCStartWindow)
			},
		},
		{
			name: "local only",
			setup: func(t *testing.T, it *idleGCTest) {
				it.c.config.LocalOnly = true
				it.clock.Advance(idleGCStartWindow)
			},
		},
		{
			name: "disabled in config",
			setup: func(t *testing.T, it *idleGCTest) {
				it.c.config.IdleLocalGC = false
				it.clock.Advance(idleGCStartWindow)
			},
		},
		{
			name: "remote unhealthy",
			setup: func(t *testing.T, it *idleGCTest) {
				it.c.setHealthStatusCode(int32(syscall.ENOTCONN), "connection refused")
				it.clock.Advance(idleGCStartWindow)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := newIdleGCTest(t, nil)
			it.c.config.IdleLocalGC = true
			old := it.putBlob(t, "old local-only output", false, time.Now().Add(2*localGCThreshold))

			tt.setup(t, it)
			ran, err := it.c.runIdleGC(context.Background())
			require.NoError(t, err)
			require.False(t, ran)
			require.NotNil(t, it.c.index.Get(old))
			require.True(t, it.hasBlob(t, old))
		})
	}
}

func TestIdleLocalGC_WaitsForUploads(t *testing.T) {
	release := make(chan struct{})
	it := newIdleGCTest(t, &blockingPutClient{ABFSClient: inmemory.New(), release: release})
	defer close(release)

	old := it.putBlob(t, "old local-only output", false, time.Now().Add(2*localGCThreshold))
	pending := objects.HashBlobBytes([]byte("waiting for upload"))
	require.NoError(t, it.c.store.WriteBlob(context.Background(), pending, bytes.NewReader([]byte("waiting for upload"))))
	it.c.getSync().PutBlob(pending)
	require.Eventually(t, func() bool {
		return !it.c.uploadsIdle()
	}, 5*time.Second, 10*time.Millisecond)

	it.clock.Advance(idleGCStartWindow)
	ran, err := it.c.runIdleGC(context.Background())
	require.NoError(t, err)
	require.False(t, ran)
	require.True(t, it.hasBlob(t, old))
}

func TestIdleLocalGC_OncePerQuietPeriod(t *testing.T) {
	it := newIdleGCTest(t, nil)
	it.clock.Advance(idleGCStartWindow)
	ran, err := it.c.runIdleGC(context.Background())
	require.NoError(t, err)
	require.True(t, ran)

	it.clock.Advance(idleGCQuietWindow)
	ran, err = it.c.runIdleGC(context.Background())
	require.NoError(t, err)
	require.False(t, ran)

	// A mount that comes and goes starts a new quiet period.
	mount := &Connection{}
	it.c.mounts.add(mount)
	it.c.mounts.markKernel(mount)
	it.c.mounts.remove(mount)
	it.clock.Advance(idleGCQuietWindow)
	ran, err = it.c.runIdleGC(context.Background())
	require.NoError(t, err)
	require.True(t, ran)

	// So does the repeat interval.
	it.clock.Advance(idleGCRepeatInterval)
	ran, err = it.c.runIdleGC(context.Background())
	require.NoError(t, err)
	require.True(t, ran)
}

func TestIdleLocalGC_PausesWhileMounted(t *testing.T) {
	it := newIdleGCTest(t, nil)
	old := it.putBlob(t, "old local-only output", false, time.Now().Add(2*localGCThreshold))
	it.clock.Advance(idleGCStartWindow)

	// A mount connects after the candidates were selected.
	mount := &Connection{}
	it.c.mounts.add(mount)
	it.c.mounts.markKernel(mount)

	done := make(chan error, 1)
	go func() {
		_, err := it.c.sweepWhenAllowed(context.Background(), []entities.Hash{old}, time.Now().Add(localGCThreshold).Unix())
		done <- err
	}()
	require.Never(t, func() bool {
		return !it.hasBlob(t, old)
	}, 2*idleGCPauseInterval, 100*time.Millisecond)

	// The sweep resumes once the mount is gone.
	it.c.mounts.remove(mount)
	it.clock.Advance(idleGCQuietWindow)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("sweep did not resume")
	}
	require.False(t, it.hasBlob(t, old))
}

func TestIdleLocalGC_KeepsMappedBlobs(t *testing.T) {
	it := newIdleGCTest(t, nil)
	old := time.Now().Add(2 * localGCThreshold)
	mapped := it.putBlob(t, "output of a step that never uploaded", false, old)
	unmapped := it.putBlob(t, "output of a failed step", false, old)

	// A live local map whose response wrote the mapped blob, stored as ninjago stores it.
	req := objects.HashBlobBytes([]byte("request"))
	resp := &binding.EvalResponse{
		EvalRequest: req,
		WritePatch: entities.Patch{
			{Path: "out/a.txt", Hash: mapped, Mode: entities.FileModeRegular},
		},
	}
	require.NoError(t, resp.Encode())
	require.NoError(t, it.c.store.PutObject(context.Background(), resp.Hash, resp.Encoded))
	require.NoError(t, it.c.maps.Put(req, resp.Hash))

	it.clock.Advance(idleGCStartWindow)
	ran, err := it.c.runIdleGC(context.Background())
	require.NoError(t, err)
	require.True(t, ran)
	require.True(t, it.hasBlob(t, mapped))
	require.False(t, it.hasBlob(t, unmapped))
}

func TestSweepKeepsBlobPutAfterSelection(t *testing.T) {
	cache, err := localcache.NewCache(t.TempDir(), localcache.CreateIfNotExistOpt(), localcache.BypassCompatCheckOpt())
	require.NoError(t, err)
	idx, err := NewIndex(&IndexOptions{
		IndexStore: NewBadgerIndexStore(t.TempDir(), nil, MaxEvictBatchSize, true),
		Cache:      cache,
	})
	require.NoError(t, err)
	defer idx.Close()

	ctx := context.Background()
	put := func(h entities.Hash, lastAlive time.Time) {
		_, _, _, _, err := idx.Put(h, &IndexEntry{
			EntryType:     EntryTypeBlob,
			Local:         true,
			Size:          1,
			LastAliveTime: lastAlive,
		})
		require.NoError(t, err)
	}
	var hashes []entities.Hash
	for i := range 3 {
		data := []byte(fmt.Sprintf("local-only %d", i))
		h := objects.HashBlobBytes(data)
		require.NoError(t, cache.WriteBlob(ctx, h, bytes.NewReader(data)))
		put(h, time.Now().Add(-48*time.Hour))
		hashes = append(hashes, h)
	}

	threshold := time.Now().Add(-time.Hour).Unix()
	candidates, err := idx.sweepCandidates(threshold)
	require.NoError(t, err)
	require.Len(t, candidates, 3)

	// A mount writes the same content again after the selection.
	put(hashes[0], time.Now())

	swept, err := idx.sweepBlobs(candidates, threshold)
	require.NoError(t, err)
	require.Equal(t, 2, swept)
	require.NotNil(t, idx.Get(hashes[0]))
	require.Nil(t, idx.Get(hashes[1]))
	require.Nil(t, idx.Get(hashes[2]))
}

func TestIdleLocalGC_SkipsMissingMapResult(t *testing.T) {
	it := newIdleGCTest(t, nil)
	old := it.putBlob(t, "output of a failed step", false, time.Now().Add(2*localGCThreshold))
	// The response of this map was evicted after its upload.
	require.NoError(t, it.c.maps.Put(hashOf("request"), hashOf("evicted response")))

	it.clock.Advance(idleGCStartWindow)
	ran, err := it.c.runIdleGC(context.Background())
	require.NoError(t, err)
	require.True(t, ran)
	require.False(t, it.hasBlob(t, old))
}

func TestIdleLocalGC_DecodeErrorDeletesNothing(t *testing.T) {
	it := newIdleGCTest(t, nil)
	old := it.putBlob(t, "output of a failed step", false, time.Now().Add(2*localGCThreshold))
	resp := hashOf("unknown response")
	require.NoError(t, it.c.store.PutObject(context.Background(), resp, []byte("not an object")))
	require.NoError(t, it.c.maps.Put(hashOf("request"), resp))

	it.clock.Advance(idleGCStartWindow)
	_, err := it.c.runIdleGC(context.Background())
	require.Error(t, err)
	require.True(t, it.hasBlob(t, old))
}

func TestIdleLocalGC_StopsDuringMark(t *testing.T) {
	it := newIdleGCTest(t, nil)
	old := it.putBlob(t, "output of a failed step", false, time.Now().Add(2*localGCThreshold))
	require.NoError(t, it.c.maps.Put(hashOf("request"), hashOf("response")))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := it.c.unmappedBlobs(ctx, []entities.Hash{old})
	require.ErrorIs(t, err, errIdleGCStopped)
}

func TestUploadsIdle(t *testing.T) {
	remote := inmemory.New()
	it := newIdleGCTest(t, remote)
	require.True(t, it.c.uploadsIdle())

	data := []byte("uploaded output")
	h := objects.HashBlobBytes(data)
	require.NoError(t, it.c.store.WriteBlob(context.Background(), h, bytes.NewReader(data)))
	it.c.getSync().PutBlob(h)
	require.Eventually(t, func() bool {
		r, err := remote.GetObject(context.Background(), h)
		if err != nil {
			return false
		}
		r.Close()
		return it.c.uploadsIdle()
	}, 5*time.Second, 10*time.Millisecond)
}

func TestEnqueueNewObjectPausesDeletes(t *testing.T) {
	it := newIdleGCTest(t, nil)
	it.clock.Advance(idleGCStartWindow)
	require.True(t, it.c.mounts.deleteAllowed())

	it.c.EnqueueNewObject(hashOf("announced"), protocol.TypeBlob)
	require.False(t, it.c.mounts.deleteAllowed())
}

func TestMaintenanceKeepsLocalOnlyAliveTime(t *testing.T) {
	it := newIdleGCTest(t, nil)
	old := time.Now().Add(2 * localGCThreshold)
	h := it.putBlob(t, "output of a failed step", false, old)

	// A cacheman start runs maintenance; it must not make old local-only blobs look new.
	require.NoError(t, it.c.maintenanceWork())
	entry := it.c.index.Get(h)
	require.NotNil(t, entry)
	require.Equal(t, old.Unix(), entry.LastAliveTime.Unix())
	require.Equal(t, int64(0), it.c.pushBlobsStat.Request.Load())

	// With eager-push on but no dagsync yet (the first pass at startup), there is no push
	// either, so the age stays.
	it.c.config.EagerPush = true
	sync := it.c.getSync()
	it.c.sync.Store(nil)
	require.NoError(t, it.c.maintenanceWork())
	require.Equal(t, old.Unix(), it.c.index.Get(h).LastAliveTime.Unix())
	require.Equal(t, int64(0), it.c.pushBlobsStat.Request.Load())

	// With eager-push on, maintenance still pushes the blob.
	it.c.sync.Store(sync)
	require.NoError(t, it.c.maintenanceWork())
	require.Equal(t, int64(1), it.c.pushBlobsStat.Request.Load())
}
