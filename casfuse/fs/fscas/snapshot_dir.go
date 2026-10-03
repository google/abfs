// Copyright 2025 Google LLC
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

package fscas

import (
	"context"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse"
)

// Simple stack item for finding dirty entries
type dirListItem struct {
	d    *dir
	next *dirListItem
}
type dirStack struct {
	top *dirListItem
}

func (s *dirStack) empty() bool {
	return s.top == nil
}
func (s *dirStack) push(d *dir) {
	obj := &dirListItem{d: d, next: s.top}
	s.top = obj
}
func (s *dirStack) pop() *dir {
	if s.top == nil {
		return nil
	}
	out := s.top.d
	s.top = s.top.next
	return out
}

func newDirStack() *dirStack {
	return &dirStack{top: nil}
}

func (d *dir) dirtyChildrenDirsLocked() bool {
	for _, entry := range *d.InternalEntriesLocked() {
		casNode, ok := entry.(Node)
		if !ok {
			// non-cas node
			continue
		}
		if !entry.Mode().IsDir() {
			continue
		}
		if casNode.IsExcluded() {
			continue
		}
		if casNode.IsDirty() {
			return true
		}
	}
	return false
}
func (d *dir) DirtyChildrenDirs(stack *dirStack) (bool, error) {
	dirty := false
	if !d.TryLock() {
		return dirty, casfuse.ErrTryAgain
	}
	defer d.Unlock()
	for _, entry := range *d.InternalEntriesLocked() {
		casNode, ok := entry.(Node)
		if !ok {
			// non-cas node
			continue
		}
		if !entry.Mode().IsDir() {
			continue
		}
		if casNode.IsExcluded() {
			continue
		}
		if casNode.IsDirty() {
			stack.push(entry.(*dir))
			dirty = true
		}
	}
	return dirty, nil
}
func (d *dir) DirtyLeafs(resolve *dirStack) error {
	stack := newDirStack()
	stack.push(d)

	for !stack.empty() {
		cur := stack.pop()
		dirty, err := cur.DirtyChildrenDirs(stack)
		if err != nil {
			if err != casfuse.ErrTryAgain {
				return err
			}
			// If we couldn't acquire the lock assume
			// that it can't be resolved
			dirty = true
			err = nil
		}
		if !dirty {
			// No dirty children this node can be resolved
			resolve.push(cur)
		}

	}
	return nil

}
func (d *dir) recalc(ctx context.Context) error {
	// called with lock
	dirtiness := d.dirtiness.Load()
	entries, err := d.snapshotDir(ctx)
	if err != nil {
		return err
	}
	hash, err := GetStore(d.FS()).SaveDirEntries(cas.NewUnsortedDirInMemory(entries))
	if err != nil {
		return err
	}
	d.hash = hash

	d.dirtiness.Add(-dirtiness)
	return nil
}
func resolveDirs(ctx context.Context, stack *dirStack) error {
	var g errgroup.Group
	for !stack.empty() {
		cur := stack.pop()
		g.Go(func() error {
			// Try to recalculate the directory hash in our list, if
			// the directory contains a dirty sub-directory it will not be
			// hashed
			if cur.TryLock() {
				defer cur.Unlock()
				if !cur.dirtyChildrenDirsLocked() {
					return cur.recalc(ctx)
				}
			}
			// if can't get lock node will be picked up on the next go
			// around
			return nil
		})
	}
	return g.Wait()
}

// Grab the directory entries with up to date hashes excluding any excluded nodes
func (d *dir) snapshotDir(ctx context.Context) ([]cas.DirEntry, error) {
	entries := d.InternalEntriesLocked()

	out := make([]cas.DirEntry, 0, len(*entries))
	keys := make([]string, 0, len(*entries))
	for k := range *entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n := (*entries)[k]
		casNode, ok := n.(Node)
		if !ok {
			continue
		}
		if casNode.IsExcluded() {
			continue
		}
		hash, size, err := casNode.Snapshot(ctx)
		if err != nil {
			return nil, err
		}

		out = append(out,
			cas.DirEntry{
				Hash:    hash,
				Size:    size,
				Mode:    casNode.Mode(),
				Name:    k,
				ExtType: casNode.ExtType(),
			},
		)
	}
	return out, nil
}

// Get full sanpshot of the directory
// This matches the implementation of the kmod version.
// Walk the directory for dirty leaf nodes these
// the edge nodes are added to out stack and then
// each one is resolved.  This process is repeated until
// the diretory is full resolved.

// Include a bail out in case the directory never goes
// clean which could happen if the user is editing the
// directory or child node constantly during the snapshot
func (d *dir) Snapshot(ctx context.Context) (hash.Hash, int64, error) {

	bailout := 5000
	count := 0
	var traverseTime time.Duration
	var resolveTime time.Duration
	stack := newDirStack()
	dirtiness := d.dirtiness.Load()
	if dirtiness == 0 {
		return d.hash, d.Size(), nil
	}
	for dirtiness != 0 {
		t1 := time.Now()
		if err := d.DirtyLeafs(stack); err != nil {
			return nil, 0, err
		}
		t2 := time.Now()
		traverseTime += t2.Sub(t1)
		if err := resolveDirs(ctx, stack); err != nil {
			return nil, 0, err
		}
		resolveTime += time.Since(t2)
		count++
		if count > bailout {
			return nil, 0, casfuse.ErrSnapshotStuck
		}
		dirtiness = d.dirtiness.Load()
	}
	return d.hash, d.Size(), nil
}

// Snapshot the names of the entriews within the directory.
// Create a sorted [] of names and pass to the CAS
// SaveDirList() interface
func (d *dir) SnapshotDirList(ctx context.Context) (hash.Hash, error) {
	d.Lock()
	defer d.Unlock()
	valid := d.validDirListHash.Load()
	if valid {
		return d.dirListHash, nil
	}
	// Add checks on error
	entries := d.InternalEntriesLocked()
	out := make([]cas.DirListEntry, 0, len(*entries))

	for name := range *entries {
		out = append(out, cas.DirListEntry{Name: name})
	}
	hash, err := GetStore(d.FS()).SaveDirList(cas.NewDirListInMemory(out))
	if err != nil {
		return nil, err
	}
	d.dirListHash = hash
	d.validDirListHash.Store(true)
	return d.dirListHash, nil
}
