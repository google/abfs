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
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/abfs/shared/dagsync"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const (
	// casfs notices a reset connection within its 1 s receive timeout, reconnects within
	// 250 ms and sends check_status on a 5 s poll, so a mount is flagged about 6.3 s
	// after a reset. No new connection for this long means no mount is connected.
	idleGCQuietWindow = 15 * time.Second
	// After a cacheman start the live mounts first have to reconnect.
	idleGCStartWindow = time.Minute
	// casfs and abfs push write a blob before they announce it, so a delete waits until
	// announcements have stopped.
	idleGCNewObjectQuiet = 10 * time.Second
	idleGCCheckInterval  = 10 * time.Second
	idleGCPauseInterval  = time.Second
	idleGCRepeatInterval = time.Hour
	// Smaller than MaxEvictBatchSize, so a mount that connects during a batch waits
	// well under a second, and so do index writes.
	idleGCBatchSize = 1000
)

var errIdleGCStopped = errors.New("cacheman is stopping")

// mountTracker tracks the connections on the cacheman socket. A connection that sends
// check_status is a kernel mount.
type mountTracker struct {
	now func() time.Time

	mu        sync.Mutex
	conns     map[*Connection]bool
	kernel    int
	started   time.Time
	lastBusy  time.Time // last accepted connection or closed mount
	lastMount time.Time // last mount seen or closed
	lastNew   time.Time // last announced new object
}

func newMountTracker(now func() time.Time) *mountTracker {
	t := now()
	return &mountTracker{
		now:      now,
		conns:    make(map[*Connection]bool),
		started:  t,
		lastBusy: t,
	}
}

func (m *mountTracker) add(c *Connection) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns[c] = false
	m.lastBusy = m.now()
}

func (m *mountTracker) markKernel(c *Connection) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if isKernel, ok := m.conns[c]; ok && !isKernel {
		m.conns[c] = true
		m.kernel++
		m.lastMount = m.now()
	}
}

func (m *mountTracker) remove(c *Connection) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	isKernel, ok := m.conns[c]
	if !ok {
		return
	}
	delete(m.conns, c)
	if isKernel {
		m.kernel--
		m.lastBusy = m.now()
		m.lastMount = m.lastBusy
	}
}

func (m *mountTracker) kernelMounts() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.kernel
}

// idleFor returns how long no mount has been connected and no connection accepted.
func (m *mountTracker) idleFor() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kernel > 0 {
		return 0
	}
	return m.now().Sub(m.lastBusy)
}

// quiet reports whether no mount can hold an object selected for the idle sweep now.
func (m *mountTracker) quiet() bool {
	return m.idleFor() >= idleGCQuietWindow && m.now().Sub(m.started) >= idleGCStartWindow
}

func (m *mountTracker) noteNewObject() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastNew = m.now()
}

// deleteAllowed reports whether no mount is connected and no new object is on its way,
// so nothing can need a blob the sweep deletes.
func (m *mountTracker) deleteAllowed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	return m.kernel == 0 && now.Sub(m.lastBusy) >= idleGCQuietWindow && now.Sub(m.lastNew) >= idleGCNewObjectQuiet
}

// mountedSince reports whether a mount was seen after t.
func (m *mountTracker) mountedSince(t time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastMount.After(t)
}

// uploadsIdle reports whether dagsync holds no upload work. Failed uploads stay
// queued and retry, so this also covers an unreachable server.
func (c *CacheMan) uploadsIdle() bool {
	sync := c.getSync()
	if sync == nil {
		return false
	}
	for state, counts := range sync.Stats().States {
		if state == dagsync.HolderState.String() {
			continue
		}
		for _, n := range counts {
			if n > 0 {
				return false
			}
		}
	}
	return true
}

func (c *CacheMan) idleGC(ctx context.Context) {
	defer c.logPanic()
	c.activeWorkers.Add(1)
	defer c.activeWorkers.Done()

	ticker := time.NewTicker(idleGCCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
		case <-c.closeChan:
			return
		case <-ctx.Done():
			return
		}
		if _, err := c.runIdleGC(ctx); err != nil {
			c.printer.Printf("Idle local object GC failed: %v\n", err)
		}
	}
}

// runIdleGC removes local-only blobs older than the local GC threshold, keeping every
// blob a live local map links to. Objects that no upload reaches (outputs of failed
// steps, for example) otherwise stay in a read-write cache for good, since eviction
// only removes objects the server holds. It starts when no mount is connected and
// deletes only while that holds. It reports whether a sweep ran.
func (c *CacheMan) runIdleGC(ctx context.Context) (bool, error) {
	config := c.config
	if !config.IdleLocalGC || !config.EnableIndex || config.LocalOnly || config.RemoteReadOnly {
		return false, nil
	}
	if !c.mounts.quiet() || c.HealthStatus() != 0 || !c.uploadsIdle() {
		return false, nil
	}
	now := c.mounts.now()
	if !c.lastIdleGC.IsZero() && !c.mounts.mountedSince(c.lastIdleGC) && now.Sub(c.lastIdleGC) < idleGCRepeatInterval {
		return false, nil
	}
	c.lastIdleGC = now

	c.printer.Println("Starting idle local object garbage collection.")
	threshold := now.Add(localGCThreshold).Unix()
	candidates, err := c.index.sweepCandidates(threshold)
	if err != nil {
		return true, err
	}
	if len(candidates) > 0 {
		candidates, err = c.unmappedBlobs(ctx, candidates)
		if errors.Is(err, errIdleGCStopped) {
			return true, nil
		} else if err != nil {
			return true, err
		}
	}
	swept, err := c.sweepWhenAllowed(ctx, candidates, threshold)
	if err != nil {
		return true, err
	}
	c.printer.Printf("Idle local object GC complete. Evicted %d objects.\n", swept)
	return true, nil
}

// sweepWhenAllowed deletes candidates batch by batch, pausing while a mount is
// connected: a mount can write an identical blob that the kernel has committed but
// cacheman has not indexed yet.
func (c *CacheMan) sweepWhenAllowed(ctx context.Context, candidates []entities.Hash, threshold int64) (int, error) {
	swept := 0
	for start := 0; start < len(candidates); start += idleGCBatchSize {
		for !c.mounts.deleteAllowed() || c.newObjReqs.Len() > 0 {
			select {
			case <-time.After(idleGCPauseInterval):
			case <-c.closeChan:
				return swept, nil
			case <-ctx.Done():
				return swept, ctx.Err()
			}
		}
		if c.isStopped(ctx) {
			return swept, nil
		}
		n, err := c.index.sweepBlobs(candidates[start:min(start+idleGCBatchSize, len(candidates))], threshold)
		swept += n
		if err != nil {
			return swept, err
		}
	}
	return swept, nil
}

// unmappedBlobs returns the candidates that no live local map links to. A cache hit
// applies the files of a mapped response by hash, so its blobs must stay while the map
// lives (DefaultTTLHours).
func (c *CacheMan) unmappedBlobs(ctx context.Context, candidates []entities.Hash) ([]entities.Hash, error) {
	set := make(map[entities.Hash]struct{}, len(candidates))
	for _, h := range candidates {
		set[h] = struct{}{}
	}
	err := c.maps.ForEach(func(from, to entities.Hash) error {
		if c.isStopped(ctx) {
			return errIdleGCStopped
		}
		content, err := c.store.GetObject(ctx, to)
		if entities.IsNotExist(err) {
			// Objects are never swept, so a missing response was evicted, which needs an
			// upload, and dagsync uploads its blobs first.
			return nil
		} else if err != nil {
			return fmt.Errorf("failed to read map result %s: %w", to, err)
		}
		// The binding package registers ninjago's response type; the abfs binary links it.
		_, obj, err := objects.Factory.Decode(to, content)
		if err != nil {
			return fmt.Errorf("failed to decode map result %s: %w", to, err)
		}
		for _, link := range obj.Links() {
			delete(set, link)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	unmapped := make([]entities.Hash, 0, len(set))
	for h := range set {
		unmapped = append(unmapped, h)
	}
	return unmapped, nil
}
