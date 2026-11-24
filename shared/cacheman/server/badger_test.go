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
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/store/localcache"
)

func nilIfZeroSize(e []entities.Hash) []entities.Hash {
	if len(e) == 0 {
		return nil
	}

	return e
}

func sortHashSlice(hashes []entities.Hash) {
	sort.Slice(hashes, func(i, j int) bool {
		return hashes[i].String() < hashes[j].String()
	})
}

func TestBadgerMapsStore(t *testing.T) {
	storeDir := t.TempDir()
	mapStore := NewBadgerMapStore(storeDir, nil, DefaultTTLHours)

	require.NoError(t, mapStore.Open())
	defer func() {
		require.NoError(t, mapStore.Close())
	}()

	key1 := entities.NewHash("deadbeef")
	tgt1 := entities.NewHash("facecafe")
	tgt2 := entities.NewHash("acabacab")
	require.NoError(t, mapStore.Put(key1, tgt1))
	require.NoError(t, mapStore.Put(key1, tgt2))

	got, err := mapStore.Get(key1)
	require.NoError(t, err)

	want := []entities.Hash{
		tgt1,
		tgt2,
	}
	sortHashSlice(got)
	sortHashSlice(want)
	require.Equal(t, nilIfZeroSize(want), nilIfZeroSize(got))

	// Test that restore works.
	require.NoError(t, mapStore.Close())
	require.NoError(t, mapStore.Open())

	got, err = mapStore.Get(key1)
	require.NoError(t, err)
	sortHashSlice(got)
	require.Equal(t, nilIfZeroSize(want), nilIfZeroSize(got))
}

func TestBadgerIndexStore(t *testing.T) {
	storeDir := t.TempDir()
	indexStore := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)

	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	entry1 := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Remote:    false,
		Size:      10,
	}
	entry2 := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Remote:    true,
		Size:      20,
	}
	key1 := entities.NewHash("deadbeef")
	key2 := entities.NewHash("facecafe")

	sz, lruSz, _, _, err := indexStore.Put(key1, entry1)
	require.NoError(t, err)
	require.Equal(t, int64(10), sz)
	require.Equal(t, int64(0), lruSz)

	sz, lruSz, _, _, err = indexStore.Put(key2, entry2)
	require.NoError(t, err)
	require.Equal(t, int64(30), sz)
	require.Equal(t, int64(20), lruSz)

	require.Equal(t, int64(30), indexStore.TotalSize())

	gotEntries, err := indexStore.ReadAll()
	require.NoError(t, err)
	want := []*IndexKeyEntry{
		{
			Key:        key1,
			IndexEntry: entry1,
		},
		{
			Key:        key2,
			IndexEntry: entry2,
		},
	}
	require.Equal(t, want, gotEntries)

	gotDelete, sz, lruSz, err := indexStore.Delete(key1)
	require.NoError(t, err)
	require.Equal(t, int64(20), sz)
	require.Equal(t, int64(20), lruSz)
	require.Equal(t, entry1, gotDelete.IndexEntry)

	// Test that restore works
	want = []*IndexKeyEntry{
		{
			Key:        key2,
			IndexEntry: entry2,
		},
	}
	require.NoError(t, indexStore.Close())
	require.NoError(t, indexStore.Open())

	gotEntries, err = indexStore.ReadAll()
	require.NoError(t, err)
	require.Equal(t, want, gotEntries)

	// Verify lruSize restoration
	require.Equal(t, int64(20), indexStore.LRUSize())
}

func TestBadgerIndexStoreUseSafety(t *testing.T) {
	storeDir := t.TempDir()
	indexStore := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)

	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	// entry1 is Local but NOT Remote
	entry1 := &IndexEntry{
		EntryType: EntryTypeObject,
		Local:     true,
		Remote:    false,
		Size:      10,
	}
	// entry2 is Local AND Remote
	entry2 := &IndexEntry{
		EntryType: EntryTypeObject,
		Local:     true,
		Remote:    true,
		Size:      20,
	}
	key1 := entities.NewHash("deadbeef")
	key2 := entities.NewHash("facecafe")

	_, lruSz, _, _, err := indexStore.Put(key1, entry1)
	require.NoError(t, err)
	require.Equal(t, int64(0), lruSz, "Local-only entry should not be in LRU")

	_, lruSz, _, _, err = indexStore.Put(key2, entry2)
	require.NoError(t, err)
	require.Equal(t, int64(20), lruSz, "Remote entry should be in LRU")

	// Use key1 (Local-only)
	err = indexStore.Use(key1)
	require.NoError(t, err)
	require.Equal(t, int64(20), indexStore.LRUSize(), "Using Local-only entry should NOT add it to LRU")

	// Use key2 (Remote)
	err = indexStore.Use(key2)
	require.NoError(t, err)
	require.Equal(t, int64(20), indexStore.LRUSize(), "Using Remote entry should maintain its LRU status")

	// Verify LRU list only contains key2
	lru, err := indexStore.ReadLRU()
	require.NoError(t, err)
	require.Equal(t, 1, len(lru))
	require.Equal(t, key2, lru[0].Key)

	// Mark key1 (Local-only) as Remote
	require.NoError(t, indexStore.MarkObjectRemote(key1))
	require.Equal(t, int64(30), indexStore.LRUSize(), "LRUSize should increase after marking remote")

	lru, err = indexStore.ReadLRU()
	require.NoError(t, err)
	require.Equal(t, 2, len(lru))
}

func TestBadgerIndexStoreMarkCandidate(t *testing.T) {
	storeDir := t.TempDir()
	indexStore := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)

	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	// Put EmptyFileHash as a local-only object
	key := entities.EmptyFileHash
	entry := &IndexEntry{
		EntryType: EntryTypeBlob,
		Local:     true,
		Remote:    false,
		Size:      0,
	}
	_, lruSz, _, _, err := indexStore.Put(key, entry)
	require.NoError(t, err)
	require.Equal(t, int64(0), lruSz)

	// Mark it Remote. Since it's not an LRU candidate (empty hash), it should NOT be added to LRU.
	require.NoError(t, indexStore.MarkObjectRemote(key))
	require.Equal(t, int64(0), indexStore.LRUSize(), "EmptyFileHash should NOT be in LRU even if Remote")

	lru, err := indexStore.ReadLRU()
	require.NoError(t, err)
	require.Equal(t, 0, len(lru), "LRU list should be empty for non-candidate hashes")
}

func TestBadgerIndexStoreMigration(t *testing.T) {
	storeDir := t.TempDir()
	indexStore := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)

	require.NoError(t, indexStore.Open())

	key1 := entities.NewHash("deadbeef")
	entry1 := &IndexEntry{
		EntryType: EntryTypeObject,
		Local:     true,
		Remote:    false,
		Size:      10,
	}
	// Manually put it in the index without LRU (normal behavior now)
	_, _, _, _, err := indexStore.Put(key1, entry1)
	require.NoError(t, err)
	require.Equal(t, int64(0), indexStore.LRUSize())

	// Simulation of legacy state: Manually add a lru:: key for a non-remote object (key1)
	// and for a non-candidate but remote object (key2 - empty hash)
	key2 := entities.EmptyFileHash
	entry2 := &IndexEntry{
		EntryType: EntryTypeBlob,
		Local:     true,
		Remote:    true,
		Size:      0,
	}
	_, _, _, _, err = indexStore.Put(key2, entry2)
	require.NoError(t, err)

	err = indexStore.db.Update(func(txn *badger.Txn) error {
		now := time.Now().Unix()
		if err := txn.Set(lruKey(now, key1), nil); err != nil {
			return err
		}
		return txn.Set(lruKey(now, key2), nil)
	})
	require.NoError(t, err)

	require.NoError(t, indexStore.Close())

	// Re-open: restoreLRU should detect that:
	// 1. key1 is NOT Remote and delete it from LRU
	// 2. key2 is NOT an LRU candidate and delete it from LRU
	indexStore = NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)
	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	require.Equal(t, int64(0), indexStore.LRUSize(), "LRUSize should be 0 after migration cleans up invalid entries")

	lru, err := indexStore.ReadLRU()
	require.NoError(t, err)
	require.Equal(t, 0, len(lru), "LRU list should be empty after migration")
}

func TestBadgerIndexStoreMerge(t *testing.T) {
	storeDir := t.TempDir()
	indexStore := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)

	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	entry1 := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Offline:   true,
		Remote:    true,
		Size:      10,
	}
	key1 := entities.NewHash("deadbeef")
	_, _, _, _, err := indexStore.Put(key1, entry1)
	require.NoError(t, err)

	// Write with remote set to false and ensure it doesn't get cleared
	entry2 := *entry1
	entry2.Remote = false
	_, _, _, _, err = indexStore.Put(key1, &entry2)
	require.NoError(t, err)

	got := indexStore.Get(key1)
	require.Equal(t, entry1, got)

	// Write with offline set to false and ensure it doesn't get cleared
	entry3 := *entry1
	entry3.Offline = false
	_, _, _, _, err = indexStore.Put(key1, &entry3)
	require.NoError(t, err)

	got = indexStore.Get(key1)
	require.Equal(t, entry1, got)
}

func TestBadgerIndexStoreMark(t *testing.T) {
	storeDir := t.TempDir()
	indexStore := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)

	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	entry1 := &IndexEntry{
		EntryType: EntryTypeObject,
		Remote:    false,
		Offline:   false,
		Size:      10,
	}
	key1 := entities.NewHash("deadbeef")
	_, _, _, _, err := indexStore.Put(key1, entry1)
	require.NoError(t, err)

	got := indexStore.Get(key1)
	require.Equal(t, entry1, got)

	require.NoError(t, indexStore.MarkObjectOffline(key1))
	got = indexStore.Get(key1)
	entry1.Offline = true
	require.Equal(t, entry1, got)

	require.NoError(t, indexStore.MarkObjectRemote(key1))
	got = indexStore.Get(key1)
	entry1.Remote = true
	require.Equal(t, entry1, got)
}

func TestBadgerIndexLRU(t *testing.T) {
	storeDir := t.TempDir()
	evictBatchSz := 1
	indexStore := NewBadgerIndexStore(storeDir, nil, uint(evictBatchSz), true)

	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	entry1 := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Remote:    false,
		Size:      10,
	}
	entry2 := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Remote:    true,
		Size:      20,
	}
	key1 := entities.NewHash("deadbeef")
	key2 := entities.NewHash("facecafe")

	_, lruSz, _, _, err := indexStore.Put(key1, entry1)
	require.Equal(t, int64(0), lruSz)
	require.NoError(t, err)
	_, lruSz, _, _, err = indexStore.Put(key2, entry2)
	require.Equal(t, int64(20), lruSz)
	require.NoError(t, err)

	require.Equal(t, int64(2), indexStore.LocalCount())

	lru, err := indexStore.ReadLRU()
	require.NoError(t, err)
	wantKeys := []entities.Hash{key2}
	var gotKeys []entities.Hash
	for _, kt := range lru {
		gotKeys = append(gotKeys, kt.Key)
	}
	require.Equal(t, wantKeys, gotKeys)

	victims, sz, lruSz, err := indexStore.Evict()
	require.NoError(t, err)
	require.Equal(t, evictBatchSz, len(victims))
	require.Equal(t, int64(10), sz)
	require.Equal(t, int64(0), lruSz)
	require.Equal(t, int64(1), indexStore.LocalCount())
	entry2.Local = false
	require.Equal(t, entry2, victims[0].IndexEntry)

	lru, err = indexStore.ReadLRU()
	require.NoError(t, err)
	wantKeys = []entities.Hash{}
	gotKeys = []entities.Hash{}
	for _, kt := range lru {
		gotKeys = append(gotKeys, kt.Key)
	}
	require.Equal(t, wantKeys, gotKeys)
}

func TestBadgerIndexStoreInMemoryFields(t *testing.T) {
	storeDir := t.TempDir()
	indexStore := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)

	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	entry1 := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Remote:    false,
		Size:      10,
	}
	entry2 := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Remote:    true,
		Size:      20,
	}
	key1 := entities.NewHash("deadbeef")
	key2 := entities.NewHash("facecafe")

	// Initial state
	require.Equal(t, int64(0), indexStore.TotalSize())
	require.Equal(t, int64(0), indexStore.LocalCount())
	require.Equal(t, int64(0), indexStore.LRUSize())
	require.Equal(t, int64(0), indexStore.IndexCount())

	// Put entry1 (Local, not Remote)
	_, _, _, _, err := indexStore.Put(key1, entry1)
	require.NoError(t, err)

	require.Equal(t, int64(10), indexStore.TotalSize())
	require.Equal(t, int64(1), indexStore.LocalCount())
	require.Equal(t, int64(0), indexStore.LRUSize())
	require.Equal(t, int64(1), indexStore.IndexCount())

	// Put entry2 (Local, Remote)
	_, _, _, _, err = indexStore.Put(key2, entry2)
	require.NoError(t, err)

	require.Equal(t, int64(30), indexStore.TotalSize())
	require.Equal(t, int64(2), indexStore.LocalCount())
	require.Equal(t, int64(20), indexStore.LRUSize())
	require.Equal(t, int64(2), indexStore.IndexCount())

	// Check timestamps
	shard1 := shardForHash(key1)
	shard2 := shardForHash(key2)

	// key1 is not remote, so it shouldn't have a timestamp
	require.Zero(t, indexStore.timestamps[shard1][key1])

	// key2 is remote, so it should have a timestamp
	require.NotZero(t, indexStore.timestamps[shard2][key2])

	// Delete key1
	_, _, _, err = indexStore.Delete(key1)
	require.NoError(t, err)

	require.Equal(t, int64(20), indexStore.TotalSize())
	require.Equal(t, int64(1), indexStore.LocalCount())
	require.Equal(t, int64(20), indexStore.LRUSize())
	require.Equal(t, int64(1), indexStore.IndexCount())
	require.Zero(t, indexStore.timestamps[shard1][key1])

	// Delete key2
	_, _, _, err = indexStore.Delete(key2)
	require.NoError(t, err)

	require.Equal(t, int64(0), indexStore.TotalSize())
	require.Equal(t, int64(0), indexStore.LocalCount())
	require.Equal(t, int64(0), indexStore.LRUSize())
	require.Equal(t, int64(0), indexStore.IndexCount())
	require.Zero(t, indexStore.timestamps[shard2][key2])
}

func TestBadgerIndexStoreRestoreFields(t *testing.T) {
	storeDir := t.TempDir()

	entry1 := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Remote:    false,
		Size:      10,
	}
	entry2 := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Remote:    true,
		Size:      20,
	}
	key1 := entities.NewHash("deadbeef")
	key2 := entities.NewHash("facecafe")

	var timestamp2 int64

	// Step 1: Create and populate the first store
	{
		store := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)
		require.NoError(t, store.Open())

		_, _, _, _, err := store.Put(key1, entry1)
		require.NoError(t, err)

		_, _, _, _, err = store.Put(key2, entry2)
		require.NoError(t, err)

		// Capture timestamp for key2
		shard2 := shardForHash(key2)
		timestamp2 = store.timestamps[shard2][key2]
		require.NotZero(t, timestamp2)

		require.NoError(t, store.Close())
	}

	// Step 2: Create a new store instance pointing to the same directory
	{
		store := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)
		require.NoError(t, store.Open())
		defer func() {
			require.NoError(t, store.Close())
		}()

		// Check fields
		require.Equal(t, int64(30), store.TotalSize())
		require.Equal(t, int64(2), store.LocalCount())
		require.Equal(t, int64(20), store.LRUSize())
		require.Equal(t, int64(2), store.IndexCount())

		// Check timestamps
		shard1 := shardForHash(key1)
		shard2 := shardForHash(key2)

		require.Zero(t, store.timestamps[shard1][key1])
		require.Equal(t, timestamp2, store.timestamps[shard2][key2])
	}
}

func TestBadgerIndexStoreRestoreWithTTLEviction(t *testing.T) {
	storeDir := t.TempDir()
	evictBatchSz := 1
	indexStore := NewBadgerIndexStore(storeDir, nil, uint(evictBatchSz), true)

	require.NoError(t, indexStore.Open())

	entry := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Remote:    true,
		Size:      20,
	}
	key := entities.NewHash("facecafe")

	_, _, _, _, err := indexStore.Put(key, entry)
	require.NoError(t, err)

	require.Equal(t, int64(20), indexStore.TotalSize())
	require.Equal(t, int64(1), indexStore.LocalCount())
	require.Equal(t, int64(20), indexStore.LRUSize())
	require.Equal(t, int64(1), indexStore.IndexCount())

	// Evict the entry. It should be set with TTL and marked non-local.
	victims, sz, lruSz, err := indexStore.Evict()
	require.NoError(t, err)
	require.Equal(t, 1, len(victims))
	require.Equal(t, int64(0), sz)
	require.Equal(t, int64(0), lruSz)
	require.Equal(t, int64(0), indexStore.TotalSize())
	require.Equal(t, int64(0), indexStore.LocalCount())
	require.Equal(t, int64(0), indexStore.LRUSize())
	require.Equal(t, int64(0), indexStore.IndexCount())

	require.NoError(t, indexStore.Close())

	// Re-open the store before TTL expires
	indexStore = NewBadgerIndexStore(storeDir, nil, uint(evictBatchSz), true)
	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	// Check counts on restart
	require.Equal(t, int64(0), indexStore.TotalSize())
	require.Equal(t, int64(0), indexStore.LocalCount())
	require.Equal(t, int64(0), indexStore.LRUSize())
	require.Equal(t, int64(0), indexStore.IndexCount(), "IndexCount should still be 0 on restart")
}

func TestBadgerIndexStorePutMany(t *testing.T) {
	storeDir := t.TempDir()
	indexStore := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)

	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	key1 := entities.NewHash("deadbeef01")
	entry1 := &IndexEntry{
		EntryType: EntryTypeBlob,
		Loose:     true,
		Local:     true,
		Remote:    false,
		Size:      100,
	}

	key2 := entities.NewHash("deadbeef02")
	entry2 := &IndexEntry{
		EntryType: EntryTypeTree,
		Loose:     true,
		Local:     true,
		Remote:    true,
		Size:      200,
	}

	key3 := entities.NewHash("deadbeef03")
	entry3 := &IndexEntry{
		EntryType: EntryTypeObject,
		Loose:     true,
		Local:     true,
		Remote:    true,
		Size:      300,
	}

	entries := []*IndexKeyEntry{
		{Key: key1, IndexEntry: entry1},
		{Key: key2, IndexEntry: entry2},
		{Key: key3, IndexEntry: entry3},
	}

	newSize, newLRUSize, err := indexStore.PutMany(entries)
	require.NoError(t, err)
	require.Equal(t, int64(600), newSize)
	require.Equal(t, int64(500), newLRUSize) // key2 (200) + key3 (300), key1 is not remote

	require.Equal(t, int64(600), indexStore.TotalSize())
	require.Equal(t, int64(500), indexStore.LRUSize())
	require.Equal(t, int64(3), indexStore.LocalCount())
	require.Equal(t, int64(3), indexStore.IndexCount())

	// Read all entries and verify
	all, err := indexStore.ReadAll()
	require.NoError(t, err)
	require.Equal(t, 3, len(all))

	// Verify LRU list only contains key2 and key3
	lru, err := indexStore.ReadLRU()
	require.NoError(t, err)
	require.Equal(t, 2, len(lru))
	var lruKeys []entities.Hash
	for _, kt := range lru {
		lruKeys = append(lruKeys, kt.Key)
	}
	sortHashSlice(lruKeys)
	wantLRU := []entities.Hash{key2, key3}
	sortHashSlice(wantLRU)
	require.Equal(t, wantLRU, lruKeys)
}

func TestBadgerIndexStorePutManyDeduplicateAndMerge(t *testing.T) {
	storeDir := t.TempDir()
	indexStore := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)

	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	key1 := entities.NewHash("deadbeef01")
	entry1a := &IndexEntry{
		EntryType: EntryTypeTree,
		Local:     true,
		Remote:    false,
		Offline:   false,
		Size:      100,
	}
	entry1b := &IndexEntry{
		EntryType: EntryTypeTree,
		Local:     true,
		Remote:    true,
		Offline:   false,
		Size:      100,
	}

	// Batch contains duplicate key1 with first Remote=false and second Remote=true
	entries := []*IndexKeyEntry{
		{Key: key1, IndexEntry: entry1a},
		{Key: key1, IndexEntry: entry1b},
	}

	newSize, newLRUSize, err := indexStore.PutMany(entries)
	require.NoError(t, err)
	require.Equal(t, int64(100), newSize)
	require.Equal(t, int64(100), newLRUSize) // merged Remote=true
	require.Equal(t, int64(1), indexStore.IndexCount())
	require.Equal(t, int64(1), indexStore.LocalCount())

	got := indexStore.Get(key1)
	require.NotNil(t, got)
	require.True(t, got.Remote)

	// Now update with Remote=false in a new PutMany, Remote state should persist (merged)
	entry1c := &IndexEntry{
		EntryType: EntryTypeTree,
		Local:     true,
		Remote:    false,
		Offline:   true,
		Size:      150,
	}
	newSize, newLRUSize, err = indexStore.PutMany([]*IndexKeyEntry{{Key: key1, IndexEntry: entry1c}})
	require.NoError(t, err)
	require.Equal(t, int64(150), newSize)
	require.Equal(t, int64(150), newLRUSize)
	require.Equal(t, int64(1), indexStore.IndexCount())

	got = indexStore.Get(key1)
	require.NotNil(t, got)
	require.True(t, got.Remote, "Remote state must persist across PutMany merge")
	require.True(t, got.Offline, "Offline state must merge")
	require.Equal(t, uint64(150), got.Size)
}

func TestBadgerIndexStorePutManyEmptyAndNil(t *testing.T) {
	storeDir := t.TempDir()
	indexStore := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)

	require.NoError(t, indexStore.Open())
	defer func() {
		require.NoError(t, indexStore.Close())
	}()

	// nil slice
	sz, lruSz, err := indexStore.PutMany(nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), sz)
	require.Equal(t, int64(0), lruSz)

	// empty slice
	sz, lruSz, err = indexStore.PutMany([]*IndexKeyEntry{})
	require.NoError(t, err)
	require.Equal(t, int64(0), sz)
	require.Equal(t, int64(0), lruSz)

	// slice with nil entry
	sz, lruSz, err = indexStore.PutMany([]*IndexKeyEntry{nil, {Key: entities.NewHash("deadbeef"), IndexEntry: nil}})
	require.NoError(t, err)
	require.Equal(t, int64(0), sz)
	require.Equal(t, int64(0), lruSz)
}

func TestBadgerIndexStorePutManyRestore(t *testing.T) {
	storeDir := t.TempDir()

	key1 := entities.NewHash("deadbeef01")
	entry1 := &IndexEntry{
		EntryType: EntryTypeBlob,
		Local:     true,
		Remote:    false,
		Size:      100,
	}

	key2 := entities.NewHash("deadbeef02")
	entry2 := &IndexEntry{
		EntryType: EntryTypeTree,
		Local:     true,
		Remote:    true,
		Size:      200,
	}

	// Step 1: PutMany and close
	{
		store := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)
		require.NoError(t, store.Open())

		_, _, err := store.PutMany([]*IndexKeyEntry{
			{Key: key1, IndexEntry: entry1},
			{Key: key2, IndexEntry: entry2},
		})
		require.NoError(t, err)
		require.NoError(t, store.Close())
	}

	// Step 2: Re-open and verify derived fields
	{
		store := NewBadgerIndexStore(storeDir, nil, MaxEvictBatchSize, true)
		require.NoError(t, store.Open())
		defer func() {
			require.NoError(t, store.Close())
		}()

		require.Equal(t, int64(300), store.TotalSize())
		require.Equal(t, int64(200), store.LRUSize())
		require.Equal(t, int64(2), store.LocalCount())
		require.Equal(t, int64(2), store.IndexCount())

		lru, err := store.ReadLRU()
		require.NoError(t, err)
		require.Equal(t, 1, len(lru))
		require.Equal(t, key2, lru[0].Key)
	}
}

func TestBadgerIndexPutManyEvictionThreshold(t *testing.T) {
	storeDir := t.TempDir()
	evictBatchSz := 2
	indexStore := NewBadgerIndexStore(storeDir, nil, uint(evictBatchSz), true)

	cacheDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	cache, err := localcache.NewCache(cacheDir, localcache.BypassCompatCheckOpt(), localcache.ExclusiveLockOpt())
	require.NoError(t, err)

	opts := &IndexOptions{
		IndexStore:     indexStore,
		Cache:          cache,
		EvictThreshold: 250, // threshold is 250 bytes
	}

	idx, err := NewIndex(opts)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, idx.Close())
	}()

	key1 := entities.NewHash("deadbeef01")
	entry1 := &IndexEntry{
		EntryType: EntryTypeObject,
		Local:     true,
		Remote:    true,
		Size:      100,
	}
	key2 := entities.NewHash("deadbeef02")
	entry2 := &IndexEntry{
		EntryType: EntryTypeObject,
		Local:     true,
		Remote:    true,
		Size:      100,
	}
	key3 := entities.NewHash("deadbeef03")
	entry3 := &IndexEntry{
		EntryType: EntryTypeObject,
		Local:     true,
		Remote:    true,
		Size:      100,
	}

	// PutMany 300 bytes total (exceeds threshold 250)
	_, _, err = idx.PutMany([]*IndexKeyEntry{
		{Key: key1, IndexEntry: entry1},
		{Key: key2, IndexEntry: entry2},
		{Key: key3, IndexEntry: entry3},
	})
	require.NoError(t, err)

	// Since eviction triggers when exceeding threshold, evictions should occur
	require.Eventually(t, func() bool {
		return idx.EvictCount() >= 1
	}, 3*time.Second, 50*time.Millisecond)
}

func TestBadgerIndexStoreUseIgnoresTombstone(t *testing.T) {
	store := NewBadgerIndexStore(t.TempDir(), nil, MaxEvictBatchSize, true)
	require.NoError(t, store.Open())
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	key := entities.NewHash("deadbeef10")
	_, _, _, _, err := store.Put(key, &IndexEntry{
		EntryType: EntryTypeBlob,
		Local:     true,
		Remote:    true,
		Size:      1000,
	})
	require.NoError(t, err)

	victims, totalSz, lruSz, err := store.Evict()
	require.NoError(t, err)
	require.Len(t, victims, 1)
	require.Equal(t, int64(0), totalSz)
	require.Equal(t, int64(0), lruSz)

	// Touching the evicted tombstone (Local=false, Remote=true) must not re-add it to LRU.
	require.NoError(t, store.Use(key))
	require.Equal(t, int64(0), store.LRUSize())

	lru, err := store.ReadLRU()
	require.NoError(t, err)
	require.Empty(t, lru)

	victims, _, _, err = store.Evict()
	require.Error(t, err)
	require.Empty(t, victims)
	require.Equal(t, int64(0), store.TotalSize())
	require.Equal(t, int64(0), store.LRUSize())
	require.Equal(t, int64(0), store.LocalCount())
	require.Equal(t, int64(0), store.IndexCount())
}

func TestBadgerIndexStoreUseIgnoresLocalOnlyAndMissing(t *testing.T) {
	store := NewBadgerIndexStore(t.TempDir(), nil, MaxEvictBatchSize, true)
	require.NoError(t, store.Open())
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	missingKey := entities.NewHash("deadbeef20")
	require.NoError(t, store.Use(missingKey))
	require.Equal(t, int64(0), store.LRUSize())

	remoteKey := entities.NewHash("deadbeef21")
	localKey1 := entities.NewHash("deadbeef22")
	localKey2 := entities.NewHash("deadbeef23")

	_, _, err := store.PutMany([]*IndexKeyEntry{
		{Key: remoteKey, IndexEntry: &IndexEntry{EntryType: EntryTypeBlob, Local: true, Remote: true, Size: 500}},
		{Key: localKey1, IndexEntry: &IndexEntry{EntryType: EntryTypeBlob, Local: true, Remote: false, Size: 1000}},
		{Key: localKey2, IndexEntry: &IndexEntry{EntryType: EntryTypeBlob, Local: true, Remote: false, Size: 2000}},
	})
	require.NoError(t, err)
	require.Equal(t, int64(500), store.LRUSize())

	// Using local-only entries must not record an LRU timestamp that Delete/DeleteMany later subtracts.
	require.NoError(t, store.Use(localKey1))
	require.NoError(t, store.Use(localKey2))
	require.Equal(t, int64(500), store.LRUSize())

	_, _, lruSz, err := store.Delete(localKey1)
	require.NoError(t, err)
	require.Equal(t, int64(500), lruSz)

	_, lruSz, err = store.DeleteMany([]entities.Hash{localKey2})
	require.NoError(t, err)
	require.Equal(t, int64(500), lruSz)
	require.Equal(t, int64(500), store.TotalSize())

	lru, err := store.ReadLRU()
	require.NoError(t, err)
	require.Len(t, lru, 1)
	require.Equal(t, remoteKey, lru[0].Key)
}

func TestBadgerIndexStoreUseRepeatTracked(t *testing.T) {
	store := NewBadgerIndexStore(t.TempDir(), nil, MaxEvictBatchSize, true)
	require.NoError(t, store.Open())
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	key := entities.NewHash("deadbeef30")
	_, lruSz, _, _, err := store.Put(key, &IndexEntry{
		EntryType: EntryTypeBlob,
		Local:     true,
		Remote:    true,
		Size:      1000,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1000), lruSz)

	lru, err := store.ReadLRU()
	require.NoError(t, err)
	require.Len(t, lru, 1)
	now := lru[0].Timestamp

	require.NoError(t, store.useWithTimestamp(key, now+10))
	require.NoError(t, store.useWithTimestamp(key, now+20))

	require.Equal(t, int64(1000), store.LRUSize())
	lru, err = store.ReadLRU()
	require.NoError(t, err)
	require.Len(t, lru, 1)
	require.Equal(t, key, lru[0].Key)
	require.Equal(t, now+20, lru[0].Timestamp)
}

func TestBadgerIndexStoreUseLocalOnlyThenPutRemote(t *testing.T) {
	store := NewBadgerIndexStore(t.TempDir(), nil, MaxEvictBatchSize, true)
	require.NoError(t, store.Open())
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	key := entities.NewHash("deadbeef31")
	_, lruSz, _, _, err := store.Put(key, &IndexEntry{
		EntryType: EntryTypeBlob,
		Local:     true,
		Remote:    false,
		Size:      1000,
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), lruSz)

	require.NoError(t, store.Use(key))
	require.Equal(t, int64(0), store.LRUSize())

	_, lruSz, _, _, err = store.Put(key, &IndexEntry{
		EntryType: EntryTypeBlob,
		Local:     true,
		Remote:    true,
		Size:      1000,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1000), lruSz)
	require.Equal(t, int64(1000), store.LRUSize())
	_, inUntracked := store.untrackedUses[shardForHash(key)][key]
	require.False(t, inUntracked)

	victims, totalSz, lruSz, err := store.Evict()
	require.NoError(t, err)
	require.Len(t, victims, 1)
	require.Equal(t, int64(0), totalSz)
	require.Equal(t, int64(0), lruSz)
	require.Equal(t, int64(0), store.LRUSize())
}

func TestBadgerIndexStoreUseUntrackedCooldown(t *testing.T) {
	store := NewBadgerIndexStore(t.TempDir(), nil, MaxEvictBatchSize, true)
	require.NoError(t, store.Open())
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	const t0 int64 = 1000

	key1 := entities.NewHash("deadbeef40")
	key2 := entities.NewHash("deadbeef41")
	key3 := entities.NewHash("deadbeef42")
	missingKey := entities.NewHash("deadbeef43")

	_, _, err := store.PutMany([]*IndexKeyEntry{
		{Key: key1, IndexEntry: &IndexEntry{EntryType: EntryTypeBlob, Local: true, Remote: false, Size: 400}},
		{Key: key2, IndexEntry: &IndexEntry{EntryType: EntryTypeBlob, Local: true, Remote: false, Size: 500}},
		{Key: key3, IndexEntry: &IndexEntry{EntryType: EntryTypeBlob, Local: true, Remote: false, Size: 600}},
	})
	require.NoError(t, err)

	// First Use on a local-only entry records the untracked cooldown without touching timestamps.
	require.NoError(t, store.useWithTimestamp(key1, t0))
	shard1 := shardForHash(key1)
	require.Equal(t, t0, store.untrackedUses[shard1][key1])
	require.Zero(t, store.timestamps[shard1][key1])

	// Flip Remote=true directly in Badger to confirm repeat Use within cooldown skips the DB read.
	err = store.db.Update(func(txn *badger.Txn) error {
		data, err := json.Marshal(&IndexEntry{EntryType: EntryTypeBlob, Local: true, Remote: true, Size: 400})
		if err != nil {
			return err
		}
		return txn.Set(entryKey(key1), data)
	})
	require.NoError(t, err)

	require.NoError(t, store.useWithTimestamp(key1, t0+useCooldownSec-1))
	require.Equal(t, int64(0), store.LRUSize())
	require.Equal(t, t0, store.untrackedUses[shard1][key1])
	lru, err := store.ReadLRU()
	require.NoError(t, err)
	require.Empty(t, lru)

	// Once the cooldown elapses, Use reads Badger, starts tracking key1, and clears untrackedUses.
	require.NoError(t, store.useWithTimestamp(key1, t0+useCooldownSec))
	require.Equal(t, int64(400), store.LRUSize())
	require.Equal(t, t0+useCooldownSec, store.timestamps[shard1][key1])
	_, ok := store.untrackedUses[shard1][key1]
	require.False(t, ok)
	lru, err = store.ReadLRU()
	require.NoError(t, err)
	require.Len(t, lru, 1)
	require.Equal(t, key1, lru[0].Key)

	// MarkObjectRemote clears untrackedUses when the entry becomes tracked.
	require.NoError(t, store.useWithTimestamp(key2, t0))
	shard2 := shardForHash(key2)
	require.Equal(t, t0, store.untrackedUses[shard2][key2])
	require.NoError(t, store.MarkObjectRemote(key2))
	_, ok = store.untrackedUses[shard2][key2]
	require.False(t, ok)
	require.NotZero(t, store.timestamps[shard2][key2])
	require.Equal(t, int64(900), store.LRUSize())

	// Delete clears untrackedUses for a local-only entry.
	require.NoError(t, store.useWithTimestamp(key3, t0))
	shard3 := shardForHash(key3)
	require.Equal(t, t0, store.untrackedUses[shard3][key3])
	_, _, _, err = store.Delete(key3)
	require.NoError(t, err)
	_, ok = store.untrackedUses[shard3][key3]
	require.False(t, ok)

	// Missing keys also obey the cooldown and clear on DeleteMany.
	shardMissing := shardForHash(missingKey)
	require.NoError(t, store.useWithTimestamp(missingKey, t0))
	require.Equal(t, t0, store.untrackedUses[shardMissing][missingKey])
	require.NoError(t, store.useWithTimestamp(missingKey, t0+useCooldownSec-1))
	require.Equal(t, t0, store.untrackedUses[shardMissing][missingKey])
	require.NoError(t, store.useWithTimestamp(missingKey, t0+useCooldownSec))
	require.Equal(t, t0+useCooldownSec, store.untrackedUses[shardMissing][missingKey])
	_, _, err = store.DeleteMany([]entities.Hash{missingKey})
	require.NoError(t, err)
	_, ok = store.untrackedUses[shardMissing][missingKey]
	require.False(t, ok)
}
