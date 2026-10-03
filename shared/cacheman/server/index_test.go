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
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/store/localcache"
)

func hashOf(s string) entities.Hash {
	return entities.Hash(sha1.Sum([]byte(s)))
}

func TestBackgroundEvictionThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	indexDir := t.TempDir()

	cache, err := localcache.NewCache(tmpDir, localcache.CreateIfNotExistOpt(), localcache.BypassCompatCheckOpt())
	require.NoError(t, err)

	indexStore := NewBadgerIndexStore(indexDir, nil, MaxEvictBatchSize, true)
	const threshold = 1000 // 1000 bytes threshold
	bgTrigger := int64(float64(threshold) * evictBackgroundTrigger)
	targetSize := int64(float64(threshold) * (1.0 - evictHeadroom))

	idx, err := NewIndex(&IndexOptions{
		IndexStore:     indexStore,
		Cache:          cache,
		EvictThreshold: threshold,
	})
	require.NoError(t, err)

	ctx := context.Background()

	// 1. Add objects up to bgTrigger threshold
	const itemSize = 100
	numItemsAtTrigger := int(bgTrigger / itemSize)
	for i := 0; i < numItemsAtTrigger; i++ {
		h := hashOf(fmt.Sprintf("obj-%d", i))
		data := bytes.Repeat([]byte{byte(i)}, itemSize)
		require.NoError(t, cache.WriteBlob(ctx, h, bytes.NewReader(data)))

		_, _, _, _, err := idx.Put(h, &IndexEntry{
			EntryType: EntryTypeBlob,
			Local:     true,
			Remote:    true,
			Size:      itemSize,
		})
		require.NoError(t, err)
	}

	require.Equal(t, int64(numItemsAtTrigger*itemSize), indexStore.LRUSize())
	require.Equal(t, int64(0), idx.EvictCount())

	// 2. Add an additional object to exceed bgTrigger
	hTrigger := hashOf("obj-trigger")
	dataTrigger := bytes.Repeat([]byte{0xff}, itemSize)
	require.NoError(t, cache.WriteBlob(ctx, hTrigger, bytes.NewReader(dataTrigger)))

	_, _, _, _, err = idx.Put(hTrigger, &IndexEntry{
		EntryType: EntryTypeBlob,
		Local:     true,
		Remote:    true,
		Size:      itemSize,
	})
	require.NoError(t, err)

	// Wait for background eviction to complete (target <= targetSize)
	require.Eventually(t, func() bool {
		return indexStore.LRUSize() <= targetSize && idx.EvictCount() > 0
	}, 3*time.Second, 50*time.Millisecond)

	// 3. Add multiple items quickly to trigger subsequent background eviction
	for i := 10; i < 15; i++ {
		h := hashOf(fmt.Sprintf("burst-obj-%d", i))
		data := bytes.Repeat([]byte{byte(i)}, itemSize)
		require.NoError(t, cache.WriteBlob(ctx, h, bytes.NewReader(data)))

		_, _, _, _, err := idx.Put(h, &IndexEntry{
			EntryType: EntryTypeBlob,
			Local:     true,
			Remote:    true,
			Size:      itemSize,
		})
		require.NoError(t, err)
	}

	// Background eviction guarantees size drops to <= targetSize
	require.Eventually(t, func() bool {
		return indexStore.LRUSize() <= targetSize
	}, 3*time.Second, 50*time.Millisecond)
}

func TestBackgroundEvictionThresholdPutMany(t *testing.T) {
	tmpDir := t.TempDir()
	indexDir := t.TempDir()

	cache, err := localcache.NewCache(tmpDir, localcache.CreateIfNotExistOpt(), localcache.BypassCompatCheckOpt())
	require.NoError(t, err)

	indexStore := NewBadgerIndexStore(indexDir, nil, MaxEvictBatchSize, true)
	const threshold = 1000 // 1000 bytes threshold
	bgTrigger := int64(float64(threshold) * evictBackgroundTrigger)
	targetSize := int64(float64(threshold) * (1.0 - evictHeadroom))

	idx, err := NewIndex(&IndexOptions{
		IndexStore:     indexStore,
		Cache:          cache,
		EvictThreshold: threshold,
	})
	require.NoError(t, err)

	ctx := context.Background()

	// 1. Add objects using PutMany to exceed bgTrigger
	const itemSize = 100
	numItems := int(bgTrigger/itemSize) + 2
	var entries []*IndexKeyEntry
	for i := 0; i < numItems; i++ {
		h := hashOf(fmt.Sprintf("putmany-obj-%d", i))
		data := bytes.Repeat([]byte{byte(i)}, itemSize)
		require.NoError(t, cache.WriteBlob(ctx, h, bytes.NewReader(data)))

		entries = append(entries, &IndexKeyEntry{
			Key: h,
			IndexEntry: &IndexEntry{
				EntryType: EntryTypeBlob,
				Local:     true,
				Remote:    true,
				Size:      itemSize,
			},
		})
	}

	_, _, err = idx.PutMany(entries)
	require.NoError(t, err)

	// Wait for background eviction to complete
	require.Eventually(t, func() bool {
		return indexStore.LRUSize() <= targetSize && idx.EvictCount() > 0
	}, 3*time.Second, 50*time.Millisecond)
}

func TestIndexReqQueue(t *testing.T) {
	q := NewIndexReqQueue()

	h1 := hashOf("h1")
	h2 := hashOf("h2")

	q.Push(IndexReq{Hash: h1, EntryType: EntryTypeBlob})
	q.Push(IndexReq{Hash: h2, EntryType: EntryTypeTree})

	req1, ok := q.Pop()
	require.True(t, ok)
	require.Equal(t, h1, req1.Hash)
	require.Equal(t, EntryTypeBlob, req1.EntryType)

	q.Close()

	// Can still pop remaining item after close
	req2, ok := q.Pop()
	require.True(t, ok)
	require.Equal(t, h2, req2.Hash)
	require.Equal(t, EntryTypeTree, req2.EntryType)

	// Queue is now empty and closed
	_, ok = q.Pop()
	require.False(t, ok)
}

func TestNewObjectReqQueue(t *testing.T) {
	q := NewNewObjectReqQueue()

	h1 := hashOf("new-h1")
	h2 := hashOf("new-h2")

	q.Push(NewObjectReq{Hash: h1})
	q.Push(NewObjectReq{Hash: h1}) // Duplicate should be dropped
	q.Push(NewObjectReq{Hash: h2})
	require.Equal(t, 2, q.Len())

	req1, ok := q.Pop()
	require.True(t, ok)
	require.Equal(t, h1, req1.Hash)

	q.Close()

	// Drain remaining
	req2, ok := q.Pop()
	require.True(t, ok)
	require.Equal(t, h2, req2.Hash)

	_, ok = q.Pop()
	require.False(t, ok)
}

func TestNotifyUseAggregator(t *testing.T) {
	agg := NewNotifyUseAggregator()

	require.Nil(t, agg.Flush())

	h1 := hashOf("use-h1")
	h2 := hashOf("use-h2")

	agg.Add(h1)
	agg.Add(h2)
	agg.Add(h1) // duplicate touch

	flushed := agg.Flush()
	require.Len(t, flushed, 2)
	require.Contains(t, flushed, h1)
	require.Contains(t, flushed, h2)

	// Second flush is empty
	require.Nil(t, agg.Flush())
}

func TestPendingAddTracker(t *testing.T) {
	tracker := NewPendingAddTracker()

	h1 := hashOf("h1")
	h2 := hashOf("h2")

	require.False(t, tracker.Has(h1))
	require.False(t, tracker.Has(h2))

	// Single add
	tracker.Add(h1)
	require.True(t, tracker.Has(h1))
	require.False(t, tracker.Has(h2))

	// Duplicate add (ref counting)
	tracker.Add(h1)
	require.True(t, tracker.Has(h1))

	// First remove leaves count at 1
	tracker.Remove(h1)
	require.True(t, tracker.Has(h1))

	// Second remove deletes the entry
	tracker.Remove(h1)
	require.False(t, tracker.Has(h1))

	// Redundant remove doesn't panic
	tracker.Remove(h1)
	require.False(t, tracker.Has(h1))
}

func TestRecentEvictionLog(t *testing.T) {
	tmpDir := t.TempDir()
	logDir := t.TempDir()
	indexDir := t.TempDir()

	cache, err := localcache.NewCache(tmpDir, localcache.CreateIfNotExistOpt(), localcache.BypassCompatCheckOpt())
	require.NoError(t, err)

	indexStore := NewBadgerIndexStore(indexDir, nil, MaxEvictBatchSize, true)
	const threshold = 1000 // 1000 bytes threshold
	bgTrigger := int64(float64(threshold) * evictBackgroundTrigger)

	idx, err := NewIndex(&IndexOptions{
		IndexStore:     indexStore,
		Cache:          cache,
		EvictThreshold: threshold,
		LogDir:         logDir,
	})
	require.NoError(t, err)

	ctx := context.Background()

	// 1. Add objects to exceed the background eviction trigger threshold
	const itemSize = 100
	numItems := int(bgTrigger/itemSize) + 1
	for i := 0; i < numItems; i++ {
		h := hashOf(fmt.Sprintf("log-test-obj-%d", i))
		data := bytes.Repeat([]byte{byte(i)}, itemSize)
		require.NoError(t, cache.WriteBlob(ctx, h, bytes.NewReader(data)))

		_, _, _, _, err := idx.Put(h, &IndexEntry{
			EntryType: EntryTypeBlob,
			Local:     true,
			Remote:    true,
			Size:      itemSize,
		})
		require.NoError(t, err)
	}

	// 2. Wait for background eviction to run and complete
	require.Eventually(t, func() bool {
		return idx.EvictCount() > 0
	}, 3*time.Second, 50*time.Millisecond)

	// 3. Verify that recent_eviction.log exists in logDir
	evictLogPath := filepath.Join(logDir, "recent_eviction.log")
	require.Eventually(t, func() bool {
		_, err := os.Stat(evictLogPath)
		return err == nil
	}, 3*time.Second, 50*time.Millisecond)

	content, err := os.ReadFile(evictLogPath)
	require.NoError(t, err)
	logStr := string(content)

	require.Contains(t, logStr, "# Eviction Run Summary")
	require.Contains(t, logStr, "Start: ")
	require.Contains(t, logStr, "End: ")
	require.Contains(t, logStr, "Duration: ")
	require.Contains(t, logStr, "StartingLRUSize: ")
	require.Contains(t, logStr, "EndingLRUSize: ")
	require.Contains(t, logStr, "TotalEvicted: ")
	require.Contains(t, logStr, "# Format: <hash> <type> <size_bytes>")
	require.Contains(t, logStr, "blob 100")
}
