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
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/dagsync"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

var (
	content1       = []byte("my sample content1")
	hash1          = objects.HashBlobBytes(content1)
	content2       = []byte("my sample content2")
	hash2          = objects.HashBlobBytes(content2)
	content1stFail = []byte("first try of push of this fails")
	hash1stFail    = objects.HashBlobBytes(content1stFail)
)

func newObject(links ...any) *gitobjects.Tree {
	tree := &object.Tree{Entries: make([]object.TreeEntry, len(links))}
	obj := gitobjects.NewTree(tree)
	for i, link := range links {
		name := fmt.Sprintf("name-%d", i)
		switch l := link.(type) {
		case entities.Hash:
			tree.Entries[i] = object.TreeEntry{Name: name, Hash: plumbing.Hash(l), Mode: filemode.Regular}
		case objects.Object:
			tree.Entries[i] = object.TreeEntry{Name: name, Hash: plumbing.Hash(l.GetHash()), Mode: filemode.Dir}
		default:
			panic(fmt.Errorf("unsupported type %T: %s", link, link))
		}
	}
	if err := obj.Encode(); err != nil {
		panic(err)
	}
	return obj
}

func assertSyncIsGood(t *testing.T, d *dagsync.DAGSync) {
	buf := new(strings.Builder)
	d.Validate(t.Context(), buf, 10*time.Second)
	require.Equal(t, "all good!\n", buf.String())
}

func TestDAGSyncNothing(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})

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

	require.NoError(t, d.Wait(10*time.Second))

	assertSyncIsGood(t, d)
}

func TestDAGSyncSimpleBlobs(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})

	accessor.blobs[hash1] = content1
	accessor.blobs[hash2] = content2

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	d.PutBlob(hash1)
	d.PutBlob(hash2)
	assertSyncIsGood(t, d)

	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	waitForObjects(t, updater, 2, 5*time.Second)
	require.NoError(t, d.Wait(10*time.Second))

	assertSyncIsGood(t, d)
}

func TestDAGSync(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})
	d.FailCooldown = time.Microsecond

	accessor.blobs[hash1] = content1
	accessor.blobs[hash2] = content2
	obj1 := newObject(hash1, hash2)
	accessor.objects[obj1.Hash] = obj1.Encoded
	obj2 := newObject(hash1, obj1)
	accessor.objects[obj2.Hash] = obj2.Encoded

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	d.PutObject(obj2.Hash)
	assertSyncIsGood(t, d)

	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	waitForObjects(t, updater, 4, 5*time.Second)
	require.NoError(t, d.Wait(10*time.Second))

	require.LessOrEqual(t, int64(2), updater.Stats().BrokenLinks)

	assertSyncIsGood(t, d)
}

func TestDAGSync2(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})
	d.FailCooldown = time.Microsecond

	accessor.blobs[hash1] = content1
	accessor.blobs[hash2] = content2
	obj1 := newObject(hash1, hash2)
	accessor.objects[obj1.Hash] = obj1.Encoded
	obj2 := newObject(hash1, obj1)
	accessor.objects[obj2.Hash] = obj2.Encoded

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	d.PutObject(obj2.Hash)
	d.PutObject(obj1.Hash)
	d.PutBlob(hash1)
	d.PutBlob(hash2)

	assertSyncIsGood(t, d)

	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	waitForObjects(t, updater, 4, 5*time.Second)
	require.NoError(t, d.Wait(10*time.Second))

	assertSyncIsGood(t, d)
}

func TestDAGSyncRetry(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})
	d.FailCooldown = time.Microsecond

	accessor.blobs[hash1] = content1
	accessor.blobs[hash1stFail] = content1stFail
	obj1 := newObject(hash1, hash1stFail)
	accessor.objects[obj1.Hash] = obj1.Encoded
	obj2 := newObject(hash1, obj1)
	accessor.objects[obj2.Hash] = obj2.Encoded

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	d.PutObject(obj2.Hash)
	assertSyncIsGood(t, d)

	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	waitForObjects(t, updater, 4, 5*time.Second)
	require.NoError(t, d.Wait(10*time.Second))

	require.LessOrEqual(t, int64(2), updater.Stats().BrokenLinks)

	assertSyncIsGood(t, d)
}

func TestDAGSyncSpecial(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	accessor.getObjDelay = 100 * time.Millisecond
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 10})

	accessor.blobs[hash1] = content1
	accessor.blobs[hash2] = content2
	obj1 := newObject(hash1)
	accessor.objects[obj1.Hash] = obj1.Encoded
	obj2 := newObject(hash2)
	accessor.objects[obj2.Hash] = obj2.Encoded

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	go func() {
		d.PutObject(obj1.Hash)
	}()
	go func() {
		d.PutObject(obj2.Hash)
	}()
	time.Sleep(50 * time.Millisecond)
	d.AddMapResult(obj1.Hash, obj2.Hash)
	assertSyncIsGood(t, d)

	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	waitForObjects(t, updater, 5, 5*time.Second)
	require.NoError(t, d.Wait(10*time.Second))

	require.LessOrEqual(t, int64(2), updater.Stats().BrokenLinks)

	assertSyncIsGood(t, d)
}

func TestPhonySuccess(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})

	accessor.blobs[hash1] = content1
	accessor.blobs[hash2] = content2
	obj1 := newObject(hash1, hash2)
	accessor.objects[obj1.Hash] = obj1.Encoded
	obj2 := newObject(hash1, obj1)
	accessor.objects[obj2.Hash] = obj2.Encoded

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	d.PutObject(obj2.Hash)
	d.PutObject(obj1.Hash)
	d.PutBlob(hash1)
	d.PutBlob(hash2)
	var wg sync.WaitGroup
	wg.Add(1)
	d.Phony(1, []entities.Hash{hash1, hash2, obj1.Hash, obj2.Hash}, func(err error) {
		// could be called with an intermediate error before the final success
		if err == nil {
			wg.Done()
		}
	})

	assertSyncIsGood(t, d)

	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	wg.Wait()

	require.NoError(t, d.Wait(10*time.Second))

	assertSyncIsGood(t, d)
}

func TestPhonyError(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	var callbackErr error
	var wg sync.WaitGroup
	wg.Add(1)
	d.Phony(1, []entities.Hash{hash1}, func(err error) {
		callbackErr = err
		wg.Done()
	})

	assertSyncIsGood(t, d)

	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	wg.Wait()
	require.Error(t, callbackErr, "expected error from the phony callback since server has no data")

	require.Error(t, d.Wait(10*time.Second), "expected error from dagsync since there's a phony node that doesn't have it's dependencies available")

	assertSyncIsGood(t, d)
}

func TestDAGLongChain(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})
	d.FailCooldown = time.Microsecond

	accessor.blobs[hash1] = content1
	accessor.blobs[hash2] = content2
	d.PutBlob(hash1)
	d.PutBlob(hash2)

	h1, h2 := hash1, hash2
	for range 1000 {
		obj := newObject(h1, h2)
		accessor.objects[obj.Hash] = obj.Encoded
		h1, h2 = h2, obj.Hash
	}
	d.PutObject(h2)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	assertSyncIsGood(t, d)

	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	waitForObjects(t, updater, 1002, 20*time.Second)
	require.NoError(t, d.Wait(20*time.Second))

	assertSyncIsGood(t, d)
}

func TestDAGPriority(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	accessor.getObjDelay = 1 * time.Millisecond
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})

	n := 1000
	var lock sync.Mutex
	var wg sync.WaitGroup
	wg.Add(n + 1)
	hashes := make([]entities.Hash, 0, n)
	dones := make([]entities.Hash, 0, n)
	for i := range n {
		content1 := fmt.Appendf(nil, "my sample content-%d", i)
		hash1 := objects.HashBlobBytes(content1)
		hashes = append(hashes, hash1)
		accessor.blobs[hash1] = content1
		d.PutBlob(hash1)
		d.Phony(0, []entities.Hash{hash1}, func(err error) {
			defer wg.Done()
			require.NoError(t, err)
			lock.Lock()
			defer lock.Unlock()
			dones = append(dones, hash1)
		})
	}
	var highPrioPos = -1
	midHash := hashes[n/2]
	d.Phony(1, []entities.Hash{midHash}, func(err error) {
		defer wg.Done()
		require.NoError(t, err)
		lock.Lock()
		defer lock.Unlock()
		highPrioPos = len(dones)
	})

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	assertSyncIsGood(t, d)

	go func() {
		d.Start(ctx)
		close(stopped)
	}()

	// we need to wait for all callbacks before calling cancel() instead of just
	// waiting for the objects to be received. There was a race condition previously
	// where dagsync sometimes processed the context cancellation before calling all
	// of the callbacks.
	wg.Wait()

	assertSyncIsGood(t, d)

	assert.Less(t, float64(highPrioPos), float64(n)*.1)
	require.NoError(t, d.Wait(10*time.Second))
}

// We had a suspected case where maintain() was cleaning up nodes too quickly, leading to missing nodes.
// This test ensures that placeholder nodes are not cleaned up more quickly than the PlaceholderNodeTimeout.
func TestPlaceholderNodeBeforeTimeout(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})
	d.FailCooldown = time.Microsecond
	d.MaintenanceRunInterval = 20 * time.Millisecond
	// huge timeout likely longer than test timeout
	d.PlaceholderNodeTimeout = 5 * time.Minute

	hashB := hash1
	accessor.blobs[hashB] = content1
	objA := newObject(hashB)
	accessor.objects[objA.Hash] = objA.Encoded

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

	// 1. Upload B first to ensure it's on the server and verified
	d.PutBlob(hashB)
	waitForObjects(t, updater, 1, 5*time.Second)

	// Wait for B to be cleaned up mainly to ensure clean state
	require.Eventually(t, func() bool {
		_, exists := d.State(hashB.String())
		return !exists
	}, 5*time.Second, 10*time.Millisecond, "Node B should be cleaned up after execution")

	// 2. Upload A, which depends on B
	// Since B is already on the server, A will succeed.
	// B will be re-created as a placeholder node because A links to it.
	d.PutObject(objA.Hash)
	// we don't want to waitForObjects() because we aren't testing if A is sent to the server; we only care about B

	require.Eventually(t, func() bool {
		_, exists := d.State(hashB.String())
		return exists
	}, 5*time.Second, 10*time.Millisecond, "node B should exist as a placeholder since it's a dependency of A")

	// sleeping for a short time so maintenance has a chance to run
	time.Sleep(100 * time.Millisecond)

	_, exists := d.State(hashB.String())
	require.True(t, exists, "Placeholder node B should still exist before timeout")

	require.NoError(t, d.Wait(10*time.Second))

	assertSyncIsGood(t, d)
}

func TestPlaceholderNodeAfterTimeout(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})
	d.FailCooldown = time.Microsecond
	d.MaintenanceRunInterval = 20 * time.Millisecond
	d.PlaceholderNodeTimeout = 100 * time.Millisecond

	hashB := hash1
	accessor.blobs[hashB] = content1
	objA := newObject(hashB)
	accessor.objects[objA.Hash] = objA.Encoded

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

	// 1. Upload B first to ensure it's on the server and verified
	d.PutBlob(hashB)
	waitForObjects(t, updater, 1, 5*time.Second)

	// Wait for B to be cleaned up mainly to ensure clean state
	require.Eventually(t, func() bool {
		_, exists := d.State(hashB.String())
		return !exists
	}, 5*time.Second, 10*time.Millisecond, "Node B should be cleaned up after execution")

	// 2. Upload A, which depends on B
	// Since B is already on the server, A will succeed.
	// B will be re-created as a placeholder node because A links to it.
	d.PutObject(objA.Hash)
	// we don't want to waitForObjects() because we aren't testing if A is sent to the server; we only care about B

	// sleeping for longer than the timeout so maintenance should clean up the node
	time.Sleep(d.PlaceholderNodeTimeout * 2)

	_, exists := d.State(hashB.String())
	require.False(t, exists, "Placeholder node B should be cleaned up after timeout")

	require.NoError(t, d.Wait(10*time.Second))

	assertSyncIsGood(t, d)
}

func TestMissingLinkRetry(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})
	d.FailCooldown = time.Microsecond
	d.MaintenanceRunInterval = 20 * time.Millisecond

	// HashB is the target of the map
	hashB := hash1
	accessor.blobs[hashB] = content1
	// HashA relies on HashB, but we will "forget" to tell dagsync about HashB
	// essentially simulating a case where the server knows better than dagsync
	hashA := hash2
	accessor.blobs[hashA] = content2

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

	// We use AddMapResult because it's a simple way to create a dependency
	// that might be checked by the server
	// HashA -> HashB
	d.AddMapResult(hashA, hashB)

	// We expect 2 objects + 1 map = 3 total counts.
	waitForObjects(t, updater, 3, 5*time.Second)
	require.NoError(t, d.Wait(10*time.Second))

	assertSyncIsGood(t, d)
}

func TestStateCountsOnTypeResolution(t *testing.T) {
	updater := inmemory.New()
	accessor := newTestAccessor()
	d := dagsync.New(&dagsync.DagSyncOpts{Updater: updater, Accessor: accessor, WorkersCount: 1})
	d.FailCooldown = time.Microsecond

	hashB := hash1
	accessor.blobs[hashB] = content1
	objA := newObject(hashB)
	accessor.objects[objA.Hash] = objA.Encoded

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

	d.PutObject(objA.Hash)

	waitForObjects(t, updater, 2, 5*time.Second)
	require.NoError(t, d.Wait(10*time.Second))

	assertSyncIsGood(t, d)

	// Verify stats have no negative values or leftover counts
	stats := d.Stats()
	for stateName, counts := range stats.States {
		for typeName, count := range counts {
			assert.Equalf(t, int64(0), count, "Expected count of %q in %q state to be 0, but got %d", typeName, stateName, count)
		}
	}
}
