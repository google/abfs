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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v4"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
)

const (
	// entries are stored with key = entry::<hash> value = IndexEntry
	// We get fast access to index entry by hash
	entryPrefix = "entry::"

	// lru items are stored with key = lru::<padded timestamp>::<hash> value = nil
	// We get fast access to the oldest entry in the list as well as to the
	// previous lru entry for a hash if the timestamp is known (tracked in `timestamps`)
	lruPrefix = "lru::"

	// Add padding to timestamps for proper range ordering behavior
	// Using 10 digits is sufficient for the next 150+ years
	lruKeyPadding = 10

	// Batch evictions for performance
	// If this value is too large, we may exceed transaction limits.
	MaxEvictBatchSize = 10000

	// The time to keep evicted entries in the index.
	EvictTTL = 24 * time.Hour

	// Time that must elapse between Uses() to update the
	// expiration time.
	useCooldownSec = 5

	// Must be a power of 2, >= 1, and <= 256
	timestampShards = 256

	noHoldFlag = false
	holdFlag   = true

	numDeletesPerTxn  = 50000
	defaultRetryDelay = 100 * time.Millisecond
	defaultNumTries   = 2
)

var _ IndexStore = (*BadgerIndexStore)(nil)
var _ MapsStore = (*BadgerMapsStore)(nil)

// BaseBadgerStore can be embedded in other structs
// for re-use.
type BaseBadgerStore struct {
	printer Printer
	db      *badger.DB
	path    string
}

// BadgerIndexStore implements IndexStore using BadgerDB as a backing store.
// Note that the notion of "Use" here is external - it's not directly
// associated with the IndexEntry usage. Rather it's the usage of the associated
// localcache object.
type BadgerIndexStore struct {
	BaseBadgerStore
	trackUse       bool
	evictBatchSize uint

	// The time a hash was last used
	timestampsLock [timestampShards]sync.Mutex
	timestamps     [timestampShards]map[entities.Hash]int64

	// 'totalSize' is an atomic int, but it is not updated
	// atomically with respect to the state of the index.
	// Callers of methods that would change the totalSize will see
	// consistent values, but concurrent TotalSize() invocations may see
	// out-of-date totalSize, if they occur in between an index
	// update and 'totalSize' update. This is acceptable for our
	// use case. 'totalSize' will be eventually consistent, and is robust against
	// crashes due to re-construction on application restart.
	// totalSize is the sum of sizes of all local objects, including those
	// tracked by LRU and those that are not.
	totalSize atomic.Int64
	// indexCount is the number of entries in the index.
	// It can have drift (be smaller than reality) when entries are marked for eviction
	// but waiting for TTL expiration. After TTL expiration, the drift is resolved.
	indexCount atomic.Int64
	// localCount is the number of objects currently stored locally. They may or
	// may not be managed by LRU.
	localCount atomic.Int64

	// lruSize is the size of entries currently managed by lru.
	lruSize atomic.Int64

	// garbage collection synchronization
	gcStart atomic.Int64
	gcEnd   atomic.Int64
	cleanMu sync.Mutex
	done    chan struct{}
	closed  bool
}

// NewBadgerIndexStore creates a BadgerIndexStore.
// On an Evict() call, up-to evictBatchSize number of keys will be evicted.
// The caller may need to re-call Evict() to reduce the size as
// desired.
func NewBadgerIndexStore(path string, printer Printer, evictBatchSize uint, trackUse bool) *BadgerIndexStore {
	if evictBatchSize == 0 || evictBatchSize > MaxEvictBatchSize {
		evictBatchSize = MaxEvictBatchSize
	}

	var timestamps [timestampShards]map[entities.Hash]int64
	for i := range timestampShards {
		timestamps[i] = make(map[entities.Hash]int64)
	}

	return &BadgerIndexStore{
		BaseBadgerStore: BaseBadgerStore{path: path, printer: PrinterOrDefault(printer)},
		trackUse:        trackUse,
		timestamps:      timestamps,
		evictBatchSize:  evictBatchSize,
		done:            make(chan struct{}, 1),
	}
}

// Open implements IndexStore.
func (b *BadgerIndexStore) Open() error {
	if b.path == "" {
		return errors.New("path field is empty")
	}
	opts := badger.DefaultOptions(b.path)
	db, err := badger.Open(opts)
	if err != nil {
		return err
	}

	b.db = db
	if err := b.updateDerivedFields(); err != nil {
		closeErr := db.Close()
		return errors.Join(err, closeErr)
	}

	go func() {
		if err := b.runGC(); err != nil {
			b.printer.Printf("runGC failed: %v\n", err)
		}
	}()

	return nil
}

// runGC cleans up the value logs once per hour.
func (b *BadgerIndexStore) runGC() error {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	closedErr := errors.New("db closed")

	onTick := func() error {
		b.cleanMu.Lock()
		defer b.cleanMu.Unlock()

		if b.closed {
			return closedErr
		}

		t := time.Now()
		b.printer.Println("Starting garbage collection on values...")

		b.gcStart.Store(time.Now().UnixNano())
		defer func() {
			b.gcEnd.Store(time.Now().UnixNano())
		}()

		err := b.db.RunValueLogGC(0.7)
		if errors.Is(err, badger.ErrNoRewrite) {
			// It's not an application error when the value log
			// doesn't require a re-write.
			err = nil
		}
		b.printer.Println("Garbage collection took", time.Since(t))
		return err
	}

	for {
		select {
		case <-ticker.C:
			err := onTick()
			if err != nil {
				// Don't bubble up a closed error
				if errors.Is(err, closedErr) {
					return nil
				}
				return err
			}
		case <-b.done:
			return nil
		}
	}
}

// Get implements IndexStore.
func (b *BadgerIndexStore) Get(hash entities.Hash) *IndexEntry {
	var entry *IndexEntry

	// intentional error ignore
	err := b.db.View(func(txn *badger.Txn) error {
		entry, _ = b.get(txn, hash)
		return nil
	})
	if err != nil {
		log.Printf("error getting entry %s: %v\n", hash, err)
		return nil
	}

	return entry
}

// Put implements IndexStore.
// There are cases where concurrent writes to the same key
// can occur.
// Put() manipulates entry:: keys. If entry is marked Remote, it also updates lru:: keys.
// Delete() manipulates both lru:: and entry:: keys
// Use() reads entry:: keys and writes lru:: keys
func (b *BadgerIndexStore) Put(hash entities.Hash, entry *IndexEntry) (newSize int64, newLRUSize int64, prevEnt, newEnt *IndexEntry, err error) {
	var (
		sizeDelta, localCountDelta, indexCountDelta int64
		lruTime                                     int64
		locked                                      bool
	)

	// We conditionally grab the timestamp lock below.
	defer func() {
		if locked {
			b.unlockTimestamps(hash)
		}
	}()

	// Not using `useWithTimestamp` because that starts a
	// separate transaction.
	maybeAddLRUItemLocked := func(localSize uint64, szDelta int64, hash entities.Hash, txn *badger.Txn) (int64, int64, error) {
		prevTimestamp := b.readTimestampLocked(hash)

		var deleteErr error
		lruSizeDelta := int64(localSize)
		// Delete the old LRU key if present.
		if prevTimestamp != 0 {
			lruSizeDelta = szDelta
			deleteErr = txn.Delete(lruKey(prevTimestamp, hash))
		}
		now := time.Now().Unix()
		addErr := txn.Set(lruKey(now, hash), nil)

		return now, lruSizeDelta, errors.Join(deleteErr, addErr)
	}

	var lruSizeDelta int64
	err = b.retryUpdateOnConflict(func(txn *badger.Txn) error {
		sizeDelta = 0
		localCountDelta = 0
		indexCountDelta = 0
		lruTime = 0
		lruSizeDelta = 0

		var hasActiveTTL bool
		prevEnt, hasActiveTTL = b.get(txn, hash)
		newEnt = maybeMergeMetadata(prevEnt, entry)

		// No need to store duplicate entry
		if prevEnt != nil && prevEnt.Equal(newEnt) {
			return nil
		}

		data, err := json.Marshal(newEnt)
		if err != nil {
			return err
		}
		err = txn.Set(entryKey(hash), data)
		if err != nil {
			return err
		}

		sizeDelta = int64(newEnt.LocalSize() - prevEnt.LocalSize())
		localCountDelta = newEnt.LocalCount() - prevEnt.LocalCount()
		// If the prev entry has an activeTTL, the indexCount was preemptively decremented
		// and we have to correct it now that we've removed the TTL
		if prevEnt == nil || hasActiveTTL {
			indexCountDelta = 1
		}
		// Add to LRU if this is already marked as remote.
		if newEnt.Remote && b.isLRUCandidate(hash) {
			// This is done in a retry loop and we need to avoid
			// trying to re-lock.
			if !locked {
				b.lockTimestamps(hash)
				locked = true
			}
			lruTime, lruSizeDelta, err = maybeAddLRUItemLocked(newEnt.LocalSize(), sizeDelta, hash, txn)
		}

		return err
	}, defaultNumTries, func() {
		if locked {
			b.unlockTimestamps(hash)
			locked = false
		}
	})
	// Tolerate a put conflict and return the existing entry without error
	// Inconsistency should be temporary as cacheman's routine maintenance
	// re-establishes index consistency.
	if errors.Is(err, ErrTxnConflict) {
		return b.totalSize.Load(), b.lruSize.Load(), nil, entry, nil
	}
	if err != nil {
		return 0, 0, nil, nil, fmt.Errorf("Put(%v) failed: %w", hash.String(), err)
	}

	// Only update the size and timestamp on successful transaction.
	newSize = b.totalSize.Add(sizeDelta)
	if lruTime != 0 {
		newLRUSize = b.lruSize.Add(lruSizeDelta)
		b.writeTimestampLocked(hash, lruTime)
	} else {
		newLRUSize = b.lruSize.Load()
	}
	b.localCount.Add(localCountDelta)
	b.indexCount.Add(indexCountDelta)

	return
}

type pendingPut struct {
	hash            entities.Hash
	data            []byte
	sizeDelta       int64
	localCountDelta int64
	indexCountDelta int64
	lruTime         int64
	lruSizeDelta    int64
	oldLRUTimestamp int64
}

// deduplicateEntries filters nil entries, deduplicates by key while merging metadata,
// and preserves the unique keys in first-seen order.
func deduplicateEntries(entries []*IndexKeyEntry) ([]entities.Hash, map[entities.Hash]*IndexEntry) {
	mergedMap := make(map[entities.Hash]*IndexEntry, len(entries))
	var uniqueHashes []entities.Hash
	for _, ke := range entries {
		if ke == nil || ke.IndexEntry == nil {
			continue
		}
		if existing, ok := mergedMap[ke.Key]; ok {
			mergedMap[ke.Key] = maybeMergeMetadata(existing, ke.IndexEntry)
		} else {
			mergedMap[ke.Key] = ke.IndexEntry
			uniqueHashes = append(uniqueHashes, ke.Key)
		}
	}
	return uniqueHashes, mergedMap
}

// preparePendingPut evaluates an entry against its existing state in BadgerDB and
// prepares the pending write and delta changes. It returns nil if the entry is identical to existing.
func (b *BadgerIndexStore) preparePendingPut(txn *badger.Txn, hash entities.Hash, entry *IndexEntry, now int64) (*pendingPut, error) {
	prevEnt, hasActiveTTL := b.get(txn, hash)
	newEnt := maybeMergeMetadata(prevEnt, entry)

	// No need to store duplicate entry
	if prevEnt != nil && prevEnt.Equal(newEnt) {
		return nil, nil
	}

	data, err := json.Marshal(newEnt)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal index entry for %s: %w", hash.String(), err)
	}

	sizeDelta := int64(newEnt.LocalSize() - prevEnt.LocalSize())
	localCountDelta := newEnt.LocalCount() - prevEnt.LocalCount()
	indexCountDelta := int64(0)
	if prevEnt == nil || hasActiveTTL {
		indexCountDelta = 1
	}

	var lruTime int64
	var lruSizeDelta int64
	var oldTimestamp int64

	if newEnt.Remote && b.isLRUCandidate(hash) {
		prevTimestamp := b.readTimestampLocked(hash)
		oldTimestamp = prevTimestamp
		lruTime = now
		if prevTimestamp != 0 {
			lruSizeDelta = sizeDelta
		} else {
			lruSizeDelta = int64(newEnt.LocalSize())
		}
	}

	return &pendingPut{
		hash:            hash,
		data:            data,
		sizeDelta:       sizeDelta,
		localCountDelta: localCountDelta,
		indexCountDelta: indexCountDelta,
		lruTime:         lruTime,
		lruSizeDelta:    lruSizeDelta,
		oldLRUTimestamp: oldTimestamp,
	}, nil
}

// preparePendingPuts reads the existing entries for unique hashes in a single View transaction
// and creates pending put operations for changed entries.
func (b *BadgerIndexStore) preparePendingPuts(uniqueHashes []entities.Hash, mergedMap map[entities.Hash]*IndexEntry) ([]pendingPut, error) {
	var puts []pendingPut
	err := b.db.View(func(txn *badger.Txn) error {
		now := time.Now().Unix()
		for _, hash := range uniqueHashes {
			put, err := b.preparePendingPut(txn, hash, mergedMap[hash], now)
			if err != nil {
				return err
			}
			if put != nil {
				puts = append(puts, *put)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("PutMany failed reading entries: %w", err)
	}
	return puts, nil
}

// writePendingPuts writes entry keys and LRU keys to BadgerDB using a WriteBatch.
func (b *BadgerIndexStore) writePendingPuts(puts []pendingPut) error {
	wb := b.db.NewWriteBatch()
	defer wb.Cancel()

	for _, p := range puts {
		if err := wb.Set(entryKey(p.hash), p.data); err != nil {
			return fmt.Errorf("failed to set entry for %s: %w", p.hash.String(), err)
		}
		if p.lruTime != 0 {
			if p.oldLRUTimestamp != 0 {
				if err := wb.Delete(lruKey(p.oldLRUTimestamp, p.hash)); err != nil {
					return fmt.Errorf("failed to delete old lru entry for %s: %w", p.hash.String(), err)
				}
			}
			if err := wb.Set(lruKey(p.lruTime, p.hash), nil); err != nil {
				return fmt.Errorf("failed to set lru entry for %s: %w", p.hash.String(), err)
			}
		}
	}

	if err := wb.Flush(); err != nil {
		return fmt.Errorf("PutMany write batch flush failed: %w", err)
	}
	return nil
}

// applyPendingPutUpdates updates in-memory timestamp tracking and atomic store counters.
func (b *BadgerIndexStore) applyPendingPutUpdates(puts []pendingPut) (newSize int64, newLRUSize int64) {
	var totalSizeDelta, totalLRUSizeDelta, totalLocalCountDelta, totalIndexCountDelta int64
	for _, p := range puts {
		totalSizeDelta += p.sizeDelta
		totalLocalCountDelta += p.localCountDelta
		totalIndexCountDelta += p.indexCountDelta
		if p.lruTime != 0 {
			totalLRUSizeDelta += p.lruSizeDelta
			b.writeTimestampLocked(p.hash, p.lruTime)
		}
	}

	newSize = b.totalSize.Add(totalSizeDelta)
	newLRUSize = b.lruSize.Add(totalLRUSizeDelta)
	b.localCount.Add(totalLocalCountDelta)
	b.indexCount.Add(totalIndexCountDelta)

	return newSize, newLRUSize
}

// PutMany implements IndexStore.
// It records multiple index entries in a single BadgerDB WriteBatch and updates LRU tracking in bulk.
func (b *BadgerIndexStore) PutMany(entries []*IndexKeyEntry) (newSize int64, newLRUSize int64, err error) {
	if len(entries) == 0 {
		return b.totalSize.Load(), b.lruSize.Load(), nil
	}

	// Deduplicate entries within the batch upfront and merge metadata if necessary.
	uniqueHashes, mergedMap := deduplicateEntries(entries)
	if len(uniqueHashes) == 0 {
		return b.totalSize.Load(), b.lruSize.Load(), nil
	}

	shards := shardsForHashes(uniqueHashes)
	b.lockTimestampShards(shards)
	defer b.unlockTimestampShards(shards)

	puts, err := b.preparePendingPuts(uniqueHashes, mergedMap)
	if err != nil {
		return 0, 0, err
	}

	if len(puts) == 0 {
		return b.totalSize.Load(), b.lruSize.Load(), nil
	}

	if err := b.writePendingPuts(puts); err != nil {
		return 0, 0, err
	}

	newSize, newLRUSize = b.applyPendingPutUpdates(puts)
	return newSize, newLRUSize, nil
}

// ForEach implements IndexStore.
func (b *BadgerIndexStore) ForEach(fn func(key entities.Hash, entry *IndexEntry, expiresAt uint64) error) error {
	err := b.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		prefix := []byte(entryPrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := item.Key()
			hash := hashFromEntryKey(key)
			entry := &IndexEntry{}
			err := item.Value(func(val []byte) error {
				if err := json.Unmarshal(val, entry); err != nil {
					return err
				}
				return nil
			})
			if err != nil {
				return err
			}

			if err := fn(hash, entry, item.ExpiresAt()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("ForEach() failed: %w", err)
	}

	return nil
}

// MarkObjectRemote implements IndexStore.
func (b *BadgerIndexStore) MarkObjectRemote(key entities.Hash) error {
	var (
		lruTime   int64
		localSize uint64
		locked    bool
	)

	defer func() {
		if locked {
			b.unlockTimestamps(key)
		}
	}()

	err := b.retryUpdateOnConflict(func(txn *badger.Txn) error {
		lruTime = 0
		localSize = 0

		curr, _ := b.get(txn, key)
		if curr == nil {
			return ErrMissingEntry
		}
		if curr.Remote {
			return nil
		}

		curr.Remote = true
		localSize = curr.LocalSize()
		data, err := json.Marshal(curr)
		if err != nil {
			return err
		}
		if err := txn.Set(entryKey(key), data); err != nil {
			return fmt.Errorf("MarkObjectRemote(%v) failed: %w", key.String(), err)
		}

		// Add to LRU now that it's remote, but only if it's a candidate.
		if b.isLRUCandidate(key) {
			if !locked {
				b.lockTimestamps(key)
				locked = true
			}
			now := time.Now().Unix()
			if err := txn.Set(lruKey(now, key), nil); err != nil {
				return err
			}
			lruTime = now
		}

		return nil
	}, defaultNumTries, func() {
		if locked {
			b.unlockTimestamps(key)
			locked = false
		}
	})

	if err == nil && lruTime != 0 {
		b.writeTimestampLocked(key, lruTime)
		b.lruSize.Add(int64(localSize))
	}
	return err
}

// MarkObjectOffline implements IndexStore.
func (b *BadgerIndexStore) MarkObjectOffline(key entities.Hash) error {
	return b.retryUpdateOnConflict(func(txn *badger.Txn) error {
		curr, _ := b.get(txn, key)
		if curr == nil {
			return ErrMissingEntry
		}
		if curr.Offline {
			return nil
		}

		curr.Offline = true
		data, err := json.Marshal(curr)
		if err != nil {
			return err
		}
		if err := txn.Set(entryKey(key), data); err != nil {
			return fmt.Errorf("MarkObjectOffline(%v) failed: %w", key.String(), err)
		}

		return nil
	}, defaultNumTries)
}

// Delete implements IndexStore.
func (b *BadgerIndexStore) Delete(hash entities.Hash) (*IndexKeyEntry, int64, int64, error) {
	var keyEntry *IndexKeyEntry
	var newSz, newLRUSz, sizeDelta, localCountDelta, indexCountDelta, lruTimeStamp int64

	b.lockTimestamps(hash)
	defer b.unlockTimestamps(hash)
	success, err := b.tryUpdateIgnoreConflict(func(txn *badger.Txn) error {
		var err error
		lruTimeStamp = b.readTimestampLocked(hash)
		keyEntry, err = b.deleteLocked(txn, hash, noHoldFlag)
		return err
	})
	if err != nil {
		return nil, 0, 0, fmt.Errorf("Delete(%v) failed: %w", hash.String(), err)
	}
	// keyEntry may be nil here.
	if keyEntry != nil {
		sizeDelta = -int64(keyEntry.LocalSize())
		localCountDelta = -keyEntry.LocalCount()
		indexCountDelta = -1
	}
	if success {
		newSz = b.totalSize.Add(sizeDelta)
		// LRU Size only get influenced if entry's already tracked.
		if lruTimeStamp > 0 {
			newLRUSz = b.lruSize.Add(sizeDelta)
		} else {
			newLRUSz = b.lruSize.Load()
		}
		b.localCount.Add(localCountDelta)
		b.indexCount.Add(indexCountDelta)
		b.deleteTimestampLocked(hash)
	} else {
		newSz = b.totalSize.Load()
		newLRUSz = b.lruSize.Load()
	}

	return keyEntry, newSz, newLRUSz, nil
}

// DeleteMany implements IndexStore.
// Multiple transactions are used if hashes is large.
func (b *BadgerIndexStore) DeleteMany(hashes []entities.Hash) (int64, int64, error) {
	var sz, lruSz int64
	var err error

	n := len(hashes)
	if n > 0 {
		for i := 0; i < n; i += numDeletesPerTxn {
			upper := min(i+numDeletesPerTxn, n)
			if sz, lruSz, err = b.deleteMany(hashes[i:upper]); err != nil {
				return 0, 0, err
			}
		}
	}

	return sz, lruSz, nil
}

// Use implements IndexStore.
// It updates the LRU key for a hash to the current timestamp.
func (b *BadgerIndexStore) Use(hash entities.Hash) error {
	if err := b.useWithTimestamp(hash, time.Now().Unix()); err != nil {
		return fmt.Errorf("Use(%v) failed: %w", hash.String(), err)
	}

	return nil
}

// UseMany implements IndexStore.
func (b *BadgerIndexStore) UseMany(hashes []entities.Hash) error {
	// This implementation issues a lot of small, individual transactions
	// concurrently. Batching can be explored if this is a performance
	// bottleneck, but each 'hash' update may read, delete, and set keys,
	// as well as manipulate the timestamps map. Large transactions here are
	// likely susceptible to transaction conflicts.
	eg := new(errgroup.Group)
	eg.SetLimit(runtime.NumCPU())
	timestamp := time.Now().Unix()

	for _, hash := range hashes {
		eg.Go(func() error {
			if err := b.useWithTimestamp(hash, timestamp); err != nil {
				return fmt.Errorf("useWithTimestamp(%v) failed: %w", hash.String(), err)
			}
			return nil
		})
	}

	return eg.Wait()
}

// Evict implements IndexStore.
func (b *BadgerIndexStore) Evict() ([]*IndexKeyEntry, int64, int64, error) {
	if !b.trackUse {
		return nil, 0, 0, errors.New("store is not configured for eviction")
	}

	var locked bool
	var lockedShards []int
	var candidates []KeyTimestamp
	var keyEntries []*IndexKeyEntry
	var sizeDelta, lruSizeDelta, localCountDelta, indexCountDelta int64

	// We conditionally grab the timestamp lock below.
	defer func() {
		if locked {
			b.unlockTimestampShards(lockedShards)
		}
	}()

	// This is not wrapped in a retry loop because index mutating
	// methods are backpressured.
	err := b.db.Update(func(txn *badger.Txn) error {
		var err error
		candidates, err = b.oldestCandidates(txn)
		if err != nil {
			return fmt.Errorf("failed to find candidates to evict: %v", err)
		}
		if len(candidates) == 0 {
			return errors.New("there is nothing to evict")
		}

		var hashes []entities.Hash
		for _, c := range candidates {
			hashes = append(hashes, c.Key)
		}

		lockedShards = shardsForHashes(hashes)
		b.lockTimestampShards(lockedShards)
		locked = true

		for _, c := range candidates {
			ke, err := b.deleteWithTimestampLocked(txn, c.Key, c.Timestamp, holdFlag)
			if err != nil {
				return err
			}
			if ke == nil {
				// We need to gracefully allow this case and not have it be an application error.
				// Just move on to the next victim
				b.printer.Printf("WARNING: %v was an orphan evict victim and was purged from LRU\n", c.Key)
				continue
			}
			keyEntries = append(keyEntries, ke)

			// We need to rely on the 'Size' variable rather than LocalSize() because we are using
			// 'holdFlag'. 'holdFlag' will keep the key in the index but mark the object non-local.
			sizeDelta -= int64(ke.Size)
			lruSizeDelta -= int64(ke.Size)
			localCountDelta -= 1
			indexCountDelta -= 1
		}
		return err
	})
	if err != nil {
		return nil, 0, 0, fmt.Errorf("Evict() failed: %w", err)
	}
	// Only update the size and timestamps on successful transaction.
	newSz := b.totalSize.Add(sizeDelta)
	newLRUSz := b.lruSize.Add(lruSizeDelta)
	b.localCount.Add(localCountDelta)
	b.indexCount.Add(indexCountDelta)
	for _, c := range candidates {
		b.deleteTimestampLocked(c.Key)
	}

	return keyEntries, newSz, newLRUSz, nil
}

// ReadAll implements IndexStore.
func (b *BadgerIndexStore) ReadAll() ([]*IndexKeyEntry, error) {
	var entries []*IndexKeyEntry

	err := b.ForEach(func(hash entities.Hash, entry *IndexEntry, expiresAt uint64) error {
		entries = append(entries, &IndexKeyEntry{Key: hash, IndexEntry: entry})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ReadAll() failed: %w", err)
	}

	return entries, nil
}

// TotalSize implements IndexStore.
func (b *BadgerIndexStore) TotalSize() int64 {
	return b.totalSize.Load()
}

// LRUSize implements IndexStore.
func (b *BadgerIndexStore) LRUSize() int64 {
	return b.lruSize.Load()
}

// LocalCount implements IndexStore.
func (b *BadgerIndexStore) LocalCount() int64 {
	return b.localCount.Load()
}

// IndexCount implements IndexStore.
func (b *BadgerIndexStore) IndexCount() int64 {
	return b.indexCount.Load()
}

// GCStart implements IndexStore.
func (b *BadgerIndexStore) GCStart() time.Time {
	return time.Unix(0, b.gcStart.Load())
}

// GCEnd implements IndexStore.
func (b *BadgerIndexStore) GCEnd() time.Time {
	return time.Unix(0, b.gcEnd.Load())
}

// ReadLRU implements IndexStore.
func (b *BadgerIndexStore) ReadLRU() ([]KeyTimestamp, error) {
	var lru []KeyTimestamp

	err := b.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(keyOnlyIterationOpts())
		defer it.Close()
		prefix := []byte(lruPrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := item.Key()
			kt, err := keyTimestampFromKey(key)
			if err != nil {
				return err
			}

			lru = append(lru, kt)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ReadLRU() failed: %w", err)
	}

	return lru, nil
}

// ReadLRU implements IndexStore.
func (b *BadgerIndexStore) PeekOldest() (KeyTimestamp, error) {
	var oldest KeyTimestamp

	err := b.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(keyOnlyIterationOpts())
		defer it.Close()
		prefix := []byte(lruPrefix)

		it.Seek(prefix)
		if !it.ValidForPrefix(prefix) {
			return errors.New("lru list is empty")
		}
		item := it.Item()
		key := item.Key()

		var err error
		oldest, err = keyTimestampFromKey(key)
		return err
	})

	return oldest, err
}

// Close implements IndexStore.
func (b *BadgerIndexStore) Close() error {
	b.cleanMu.Lock()
	defer func() {
		b.closed = true
		b.done <- struct{}{}
		b.cleanMu.Unlock()
	}()

	return b.db.Close()
}

// KeepAlive implements IndexStore.
func (b *BadgerIndexStore) KeepAlive(key entities.Hash) error {
	return b.retryUpdateOnConflict(func(txn *badger.Txn) error {
		curr, _ := b.get(txn, key)
		if curr == nil {
			return nil
		}
		curr.LastAliveTime = time.Now()
		data, err := json.Marshal(curr)
		if err != nil {
			return err
		}
		if err := txn.Set(entryKey(key), data); err != nil {
			return fmt.Errorf("KeepAlive(%v) failed: %w", key.String(), err)
		}

		return nil
	}, defaultNumTries)
}

// Returns up-to `batchSize` oldest candidates (hash and timestamp).
// Should only be called from the context of an open transaction.
func (b *BadgerIndexStore) oldestCandidates(txn *badger.Txn) ([]KeyTimestamp, error) {
	var candidates []KeyTimestamp

	it := txn.NewIterator(keyOnlyIterationOpts())
	defer it.Close()
	prefix := []byte(lruPrefix)
	for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
		item := it.Item()
		kt, err := keyTimestampFromKey(item.Key())
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, kt)

		if len(candidates) == int(b.evictBatchSize) {
			break
		}
	}

	return candidates, nil
}

// get returns a nil entry on error.
// Should only be called from the context of an open transaction.
func (b *BadgerIndexStore) get(txn *badger.Txn, hash entities.Hash) (*IndexEntry, bool) {
	item, err := txn.Get(entryKey(hash))
	if err != nil {
		return nil, false
	}

	entry := &IndexEntry{}
	err = item.Value(func(val []byte) error {
		return json.Unmarshal(val, entry)
	})
	if err != nil {
		return nil, false
	}

	return entry, item.ExpiresAt() > 0
}

// deleteWithTimestampLocked removes the hash from index entries and cleans up LRU timestamps
// using the provided timestamp.
// Caller should be holding the timestamps lock for the hash, and must delete
// from the timestamps map if the txn completes successfully.
// It returns the deleted entry, or nil if one is not found.
// Should only be called from the context of an open transaction.
// If 'hold' is true, the index entry will remain for `EvictTTL` duration.
// * Note that even though the entry is still present in the index, it is no longer
// * reachable from an eviction process because it is purged from the LRU list.
func (b *BadgerIndexStore) deleteWithTimestampLocked(txn *badger.Txn, hash entities.Hash, timestamp int64, hold bool) (*IndexKeyEntry, error) {
	entry, _ := b.get(txn, hash)
	// If we cannot find the entry in the entry:: keyspace, but
	// we were provided a timestamp, it means the entry was orphaned
	// in the lru:: keyspace. We need to purge it.
	if entry == nil {
		if timestamp != 0 {
			lruErr := txn.Delete(lruKey(timestamp, hash))
			return nil, lruErr
		}
		return nil, nil
	}

	var entryErr error
	if hold {
		// Mark not local and set TTL.
		entry.Local = false
		data, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		e := badger.NewEntry(entryKey(hash), data).WithTTL(EvictTTL)
		entryErr = txn.SetEntry(e)
	} else {
		entryErr = txn.Delete(entryKey(hash))
	}

	lruErr := txn.Delete(lruKey(timestamp, hash))

	keyEntry := &IndexKeyEntry{Key: hash, IndexEntry: entry}
	return keyEntry, errors.Join(entryErr, lruErr)
}

// deleteLocked behaves the same as deleteWithTimestampLocked, but it searches
// for the timestamp rather than accepting one.
func (b *BadgerIndexStore) deleteLocked(txn *badger.Txn, hash entities.Hash, hold bool) (*IndexKeyEntry, error) {
	timestamp := b.readTimestampLocked(hash)
	return b.deleteWithTimestampLocked(txn, hash, timestamp, hold)
}

func addPrefix(prefix string, hash entities.Hash) []byte {
	return append([]byte(prefix), []byte(hash.String())...)
}

func entryKey(hash entities.Hash) []byte {
	return addPrefix(entryPrefix, hash)
}

func lruKey(timestamp int64, hash entities.Hash) []byte {
	return []byte(fmt.Sprintf("%s%0*d::%s", lruPrefix, lruKeyPadding, timestamp, hash.String()))
}

func stripPrefix(prefix string, key []byte) entities.Hash {
	b := bytes.TrimPrefix(key, []byte(prefix))
	return entities.NewHash(string(b))
}

func hashFromEntryKey(key []byte) entities.Hash {
	return stripPrefix(entryPrefix, key)
}

// restoreIndexStats re-populates the in-memory index stats value from the state of the
// store.
func (b *BadgerIndexStore) restoreIndexStats() error {
	var sz, objCount, indexCount int64
	err := b.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		prefix := []byte(entryPrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			// If the item is expired, it has been evicted and waiting for
			// TTL expiration. The items aren't local and the indexCount was
			// decremented at the time of eviction so we should ignore it here.
			if item.ExpiresAt() > 0 {
				continue
			}
			err := item.Value(func(val []byte) error {
				entry := &IndexEntry{}
				if err := json.Unmarshal(val, entry); err != nil {
					return err
				}
				sz += int64(entry.LocalSize())
				objCount += entry.LocalCount()
				indexCount++
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	b.totalSize.Store(sz)
	b.localCount.Store(objCount)
	b.indexCount.Store(indexCount)
	return nil
}

// restoreLRUFromLRUKeys re-populates the in-memory timestamp map and calculates initial lruSize
// by iterating over the lru:: keyspace.
func (b *BadgerIndexStore) restoreLRUFromLRUKeys() (int64, error) {
	var lruSz int64
	err := b.db.Update(func(txn *badger.Txn) error {
		it := txn.NewIterator(keyOnlyIterationOpts())
		defer it.Close()
		prefix := []byte(lruPrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := item.Key()
			parts := bytes.Split(key, []byte("::"))
			if len(parts) != 3 {
				b.printer.Printf("WARNING: deleting malformed LRU key: %v\n", string(key))
				if err := txn.Delete(key); err != nil {
					return err
				}
				continue
			}
			timestamp, err := strconv.ParseInt(string(parts[1]), 10, 64)
			if err != nil {
				b.printer.Printf("WARNING: deleting LRU key with invalid timestamp: %v\n", string(key))
				if err := txn.Delete(key); err != nil {
					return err
				}
				continue
			}
			hash := entities.NewHash(string(parts[2]))

			entry, _ := b.get(txn, hash)
			// Migration/Cleanup: If entry doesn't exist, is not Remote, or is not an LRU candidate, it shouldn't be in LRU.
			if entry == nil || !entry.Remote || !b.isLRUCandidate(hash) {
				if err := txn.Delete(key); err != nil {
					return err
				}
				continue
			}

			lruSz += int64(entry.LocalSize())

			// We aren't holding the lock here but this is
			// done serially during restore and before concurrent
			// accesses occur
			b.writeTimestampLocked(hash, timestamp)
		}
		return nil
	})
	return lruSz, err
}

// ensureLRUConsistency ensures that all entries that should be in the LRU are present
// by iterating over the entry:: keyspace and adding missing ones.
// It returns the delta size of newly added entries.
// The entry:: keys are iterated in a RO transaction and the writes happen in a manually
// managed write transactions. The write transactions are batched to avoid too-large-txn
// errors.
func (b *BadgerIndexStore) ensureLRUConsistency() (int64, error) {
	const batchSize = 10000
	var delta int64
	err := b.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		prefix := []byte(entryPrefix)

		var writeTxn *badger.Txn
		var count int

		commit := func() error {
			if writeTxn != nil {
				err := writeTxn.Commit()
				writeTxn.Discard()
				writeTxn = nil
				return err
			}
			return nil
		}

		ensureTxn := func() *badger.Txn {
			if writeTxn == nil {
				writeTxn = b.db.NewTransaction(true)
			}
			return writeTxn
		}

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := item.Key()
			hash := hashFromEntryKey(key)

			entry := &IndexEntry{}
			err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, entry)
			})
			if err != nil {
				continue // Skip malformed entries
			}

			if entry.Local && entry.Remote && b.isLRUCandidate(hash) {
				// If it's not in the timestamp map, it's missing from LRU
				if b.readTimestampLocked(hash) == 0 {
					now := time.Now().Unix()

					txnToUse := ensureTxn()
					if err := txnToUse.Set(lruKey(now, hash), nil); err != nil {
						return err
					}
					b.writeTimestampLocked(hash, now)
					delta += int64(entry.LocalSize())

					count++
					if count >= batchSize {
						if err := commit(); err != nil {
							return err
						}
						count = 0
					}
				}
			}
		}

		return commit()
	})
	return delta, err
}

// restoreLRU re-populates the in-memory timestamp map and lruSize from the state of the
// store. It calls restoreLRUFromLRUKeys and ensureLRUConsistency.
func (b *BadgerIndexStore) restoreLRU() error {
	lruSz, err := b.restoreLRUFromLRUKeys()
	if err != nil {
		return err
	}
	delta, err := b.ensureLRUConsistency()
	if err != nil {
		return err
	}

	b.lruSize.Store(lruSz + delta)
	return nil
}

func (b *BadgerIndexStore) updateDerivedFields() error {
	if err := b.restoreIndexStats(); err != nil {
		return err
	}
	if err := b.restoreLRU(); err != nil {
		return err
	}

	return nil
}

func (b *BadgerIndexStore) isLRUCandidate(hash entities.Hash) bool {
	if !b.trackUse {
		return false
	}

	// The empty file and dir hashes don't need LRU tracking.
	if hash == entities.EmptyFileHash || hash == entities.EmptyDirHash {
		return false
	}

	return true
}

// useWithTimestamp makes best effort to Use the input hash and does not report an error
// on a conflict.
func (b *BadgerIndexStore) useWithTimestamp(hash entities.Hash, currTimestamp int64) error {
	// sizeDelta will be zero if the entry is already in LRU tracking
	// otherwise, it will be LocalSize of the entry
	var sizeDelta int64
	if !b.isLRUCandidate(hash) {
		return nil
	}

	b.lockTimestamps(hash)
	defer b.unlockTimestamps(hash)

	prevTimestamp := b.readTimestampLocked(hash)
	// Limit spammy Use() calls.
	if (currTimestamp - prevTimestamp) < useCooldownSec {
		return nil
	}

	// Use accesses both entry:: and lru:: timestamps
	// We're protected from concurrent lru:: accesses with
	// the timestamps lock, but entries we're not.
	// If there's a concurrent entry writing transaction,
	// it also will write a lru entry on first entry creation.
	// This will just be best effort.
	success, err := b.tryUpdateIgnoreConflict(func(txn *badger.Txn) error {
		// Don't allow Use of an object not currently in the index.
		// Use() and Put() are concurrent with each other, so it's possible
		// the entry may not exist yet. The LRU will be updated on a future Put()
		// call. 'maintenance' will also re-establish consistency.
		entry, _ := b.get(txn, hash)
		if entry == nil {
			return nil
		}

		// Only add to LRU if it is a Remote object.
		if !entry.Remote {
			return nil
		}

		sizeDelta += int64(entry.LocalSize())

		var deleteErr error
		// Delete the old LRU key if present.
		if prevTimestamp != 0 {
			sizeDelta = 0
			deleteErr = txn.Delete(lruKey(prevTimestamp, hash))
		}

		addErr := txn.Set(lruKey(currTimestamp, hash), nil)

		return errors.Join(deleteErr, addErr)
	})
	if err != nil {
		return err
	}
	if success {
		b.writeTimestampLocked(hash, currTimestamp)
		b.lruSize.Add(sizeDelta)
	}

	return nil
}

func shardForHash(hash entities.Hash) int {
	return int(hash[0]) & (timestampShards - 1)
}

func shardsForHashes(hashes []entities.Hash) []int {
	m := map[int]bool{}
	shards := []int{}
	for _, hash := range hashes {
		shard := shardForHash(hash)
		if m[shard] {
			continue
		}

		m[shard] = true
		shards = append(shards, shard)
	}
	sort.Ints(shards)
	return shards
}

func (b *BadgerIndexStore) lockTimestamps(hash entities.Hash) {
	shard := shardForHash(hash)
	b.timestampsLock[shard].Lock()
}

func (b *BadgerIndexStore) lockTimestampShards(shards []int) {
	for _, shard := range shards {
		b.timestampsLock[shard].Lock()
	}
}

func (b *BadgerIndexStore) unlockTimestamps(hash entities.Hash) {
	shard := shardForHash(hash)
	b.timestampsLock[shard].Unlock()
}

func (b *BadgerIndexStore) unlockTimestampShards(shards []int) {
	for _, shard := range shards {
		b.timestampsLock[shard].Unlock()
	}
}

func (b *BadgerIndexStore) readTimestampLocked(hash entities.Hash) int64 {
	shard := shardForHash(hash)
	return b.timestamps[shard][hash]
}

func (b *BadgerIndexStore) writeTimestampLocked(hash entities.Hash, timestamp int64) {
	shard := shardForHash(hash)
	b.timestamps[shard][hash] = timestamp
}

func (b *BadgerIndexStore) deleteTimestampLocked(hash entities.Hash) {
	shard := shardForHash(hash)
	delete(b.timestamps[shard], hash)
}

// Transaction conflicts can occur when:
// 1. Concurrent writes are detected to the same key
// 2. A read key in an 'Update' transaction is written in another concurrent transaction
// retryUpdateOnConflict will execute 'fn' inside an Update (which will commit) a configurable number
// of times before failing. This function is not suitable for cases where 'fn' will
// exceed the maximum transaction size limit.
func (b *BadgerIndexStore) retryUpdateOnConflict(fn func(txn *badger.Txn) error, numTries int, onConflict ...func()) error {
	var err error
	for i := range numTries {
		if i > 0 {
			time.Sleep(defaultRetryDelay)
		}

		err = b.db.Update(fn)
		if err == nil { // If NO error: done.
			break
		}
		// Don't allow retry if error isn't related to a conflict.
		if !errors.Is(err, badger.ErrConflict) {
			break
		}
		for _, f := range onConflict {
			f()
		}
	}

	// Bubble up generic conflict error
	if errors.Is(err, badger.ErrConflict) {
		return ErrTxnConflict
	}

	return err
}

// tryUpdateIgnoreConflict will attempt to execute a transaction, but won't return an application
// error on a conflict. This function is not suitable for cases where 'fn' will
// exceed the maximum transaction size limit.
func (b *BadgerIndexStore) tryUpdateIgnoreConflict(fn func(txn *badger.Txn) error) (bool, error) {
	err := b.db.Update(fn)
	if err == nil { // If NO error: done.
		return true, nil
	}

	// Do not bubble up a conflict error, but indicate that the transaction
	// did not apply.
	if errors.Is(err, badger.ErrConflict) {
		return false, nil
	}

	return false, err
}

func (b *BadgerIndexStore) deleteMany(hashes []entities.Hash) (int64, int64, error) {
	var newSz, newLRUSz, sizeDelta, localCountDelta, indexCountDelta, lruSizeDelta int64

	shards := shardsForHashes(hashes)
	b.lockTimestampShards(shards)
	defer b.unlockTimestampShards(shards)

	success, err := b.tryUpdateIgnoreConflict(func(txn *badger.Txn) error {
		for _, hash := range hashes {
			lruTimeStamp := b.readTimestampLocked(hash)
			ke, err := b.deleteLocked(txn, hash, noHoldFlag)
			if err != nil {
				return err
			}
			if ke == nil {
				continue
			}
			sizeDelta -= int64(ke.LocalSize())
			if lruTimeStamp > 0 {
				lruSizeDelta -= int64(ke.LocalSize())
			}
			localCountDelta -= ke.LocalCount()
			indexCountDelta -= 1
		}
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("DeleteMany() failed: %w", err)
	}
	if success {
		newSz = b.totalSize.Add(sizeDelta)
		newLRUSz = b.lruSize.Add(lruSizeDelta)
		b.localCount.Add(localCountDelta)
		b.indexCount.Add(indexCountDelta)
		for _, hash := range hashes {
			b.deleteTimestampLocked(hash)
		}
	} else {
		newSz = b.totalSize.Load()
		newLRUSz = b.lruSize.Load()
	}

	return newSz, newLRUSz, nil
}

// BadgerMapsStore implements MapsStore using BadgerDB as a backing store.
// It does not require any GC because the value space is all nil.
type BadgerMapsStore struct {
	BaseBadgerStore
	ttlHours time.Duration
}

func NewBadgerMapStore(path string, printer Printer, ttlHours time.Duration) *BadgerMapsStore {
	return &BadgerMapsStore{
		BaseBadgerStore: BaseBadgerStore{path: path, printer: PrinterOrDefault(printer)},
		ttlHours:        ttlHours,
	}
}

// Open implements MapsStore.
func (b *BadgerMapsStore) Open() error {
	if b.path == "" {
		return errors.New("path field is empty")
	}
	opts := badger.DefaultOptions(b.path)
	db, err := badger.Open(opts)
	if err != nil {
		return err
	}

	b.db = db
	return nil
}

// Get implements MapsStore.
func (b *BadgerMapsStore) Get(from entities.Hash) ([]entities.Hash, error) {
	var hashes []entities.Hash
	err := b.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(keyOnlyIterationOpts())
		defer it.Close()
		prefix := []byte(from.String())
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := item.Key()
			parts := bytes.Split(key, []byte("::"))
			if len(parts) != 2 {
				return fmt.Errorf("could not parse key: %v", string(key))
			}
			hashes = append(hashes, entities.NewHash(string(parts[1])))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return hashes, nil
}

// Put implements MapsStore.
func (b *BadgerMapsStore) Put(from, to entities.Hash) error {
	key := fmt.Sprintf("%s::%s", from.String(), to.String())
	return b.db.Update(func(txn *badger.Txn) error {
		e := badger.NewEntry([]byte(key), nil).WithTTL(b.ttlHours * time.Hour)
		if err := txn.SetEntry(e); err != nil {
			return fmt.Errorf("Put(%v, %v) failed: %w", from.String(), to.String(), err)
		}
		return nil
	})
}

// Close implements MapsStore.
func (b *BadgerMapsStore) Close() error {
	return b.db.Close()
}

// https://docs.hypermode.com/badger/quickstart#key-only-iteration
// For key spaces where we do not care about the value, iterating over
// the key space only is more performant. We ignore the value prefetching.
func keyOnlyIterationOpts() badger.IteratorOptions {
	opts := badger.DefaultIteratorOptions
	opts.PrefetchValues = false
	return opts
}

func maybeMergeMetadata(curr *IndexEntry, entry *IndexEntry) *IndexEntry {
	if curr == nil {
		return entry
	}

	merged := entry.ShallowCopy()
	switch merged.EntryType {
	case EntryTypeTree, EntryTypeBlob, EntryTypeObject, EntryTypeARI:
		// For objects, with immutable contents, the Remote state must persist if it was set prior.
		// We allow 'Local' to get overwritten since it can get evicted and/or re-cached.
		merged.Remote = curr.Remote || entry.Remote
		// We also want Offline to persist to prevent it to be uploaded
		merged.Offline = curr.Offline || entry.Offline
	default:
	}

	return &merged
}

func keyTimestampFromKey(key []byte) (KeyTimestamp, error) {
	parts := bytes.Split(key, []byte("::"))
	if len(parts) != 3 {
		return KeyTimestamp{}, fmt.Errorf("could not parse key: %v", string(key))
	}
	hash := entities.NewHash(string(parts[2]))
	timestamp, err := strconv.ParseInt(string(parts[1]), 10, 64)
	if err != nil {
		return KeyTimestamp{}, fmt.Errorf("could not parse timestamp: %v", string(parts[1]))
	}

	return KeyTimestamp{Key: hash, Timestamp: timestamp}, nil
}
