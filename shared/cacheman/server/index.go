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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dustin/go-humanize"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/store/localcache"
)

// We evict an extra amount of objects when we're at the threshold
// in order to reduce thrashing (evict down to 80% of evictThreshold)
const evictHeadroom = 0.2

// Background eviction starts at 90% of evictThreshold
const evictBackgroundTrigger = 0.9

const indexDebug = false

var ErrMissingEntry = errors.New("missing index entry")
var ErrTxnConflict = errors.New("transaction conflict")
var ErrTxnTooBig = errors.New("transaction too big")

// IndexStore implements the API to access
// index entries and support LRU functionality.
type IndexStore interface {
	// Open must be called on any store prior to use.
	Open() error

	// Get returns the index entry associated with the hash, or
	// nil if one cannot be found.
	Get(hash entities.Hash) *IndexEntry

	// Put adds an index entry to the store and returns both the updated total size of the index,
	// and the updated total LRU Size.
	// If the entry already exists, it is not completely replaced, but rather merged.
	// This prevents clobbering of state like 'Remote'.
	// Callers may want to implement a retry mechanism in case of transaction conflicts.
	Put(hash entities.Hash, entry *IndexEntry) (newSize int64, newLRUSize int64, prevEnt, newEnt *IndexEntry, err error)

	// PutMany adds multiple index entries to the store and returns both the updated total size of the index,
	// and the updated total LRU Size.
	PutMany(entries []*IndexKeyEntry) (newSize int64, newLRUSize int64, err error)

	// ForEach iterates over the keys and values and calls the provided function.
	ForEach(fn func(key entities.Hash, entry *IndexEntry, expiresAt uint64) error) error

	// MarkObjectRemote marks an existing entry as Remote.
	MarkObjectRemote(key entities.Hash) error

	// MarkObjectOffline marks an existing entry as Offline.
	MarkObjectOffline(key entities.Hash) error

	// Delete removes the index entry and LRU information associated with the hash.
	// It returns the deleted key and the index size after the deletion.
	// Returns nil on non-existent entry.
	Delete(hash entities.Hash) (*IndexKeyEntry, int64, int64, error)

	// DeleteMany removes multiple hashes from the index.
	// It returns the index size and lru size after all the deletions.
	// Non-existent entries are ignored.
	DeleteMany(hashes []entities.Hash) (int64, int64, error)

	// Use should be called to update LRU tracking information.
	Use(hash entities.Hash) error

	// UseMany can be used to "touch" multiple objects.
	UseMany(hashes []entities.Hash) error

	// Evict returns the evicted key entries and the size of the index after
	// the deletion.
	Evict() ([]*IndexKeyEntry, int64, int64, error)

	// ReadAll returns all index entries.
	ReadAll() ([]*IndexKeyEntry, error)

	// ReadLRU returns the LRU list in oldest->newest order.
	ReadLRU() ([]KeyTimestamp, error)

	// PeekOldest returns the oldest item in the LRU list.
	PeekOldest() (KeyTimestamp, error)

	// TotalSize returns the size of the entries tracked by the index,
	// not the size of the index metadata itself.
	TotalSize() int64

	// LRUSize returns the size of the entries tracked in LRU.
	LRUSize() int64

	LocalCount() int64

	IndexCount() int64

	GCStart() time.Time
	GCEnd() time.Time

	// Close closes the store.
	Close() error

	// KeepAlive should be called to update the last alive time of the entry.
	KeepAlive(key entities.Hash) error
}

type EntryType int

const (
	EntryTypeInvalid EntryType = iota
	EntryTypeBlob
	EntryTypeTree
	EntryTypeObject
	EntryTypeARI
)

func AllEntryTypes() []EntryType {
	return []EntryType{EntryTypeBlob, EntryTypeTree, EntryTypeObject, EntryTypeARI}
}

func ObjectTypeToEntryType(objType string) EntryType {
	switch objType {
	case "blob", "cas_blob":
		return EntryTypeBlob
	case "tree":
		return EntryTypeTree
	case "ari":
		return EntryTypeARI
	default:
		return EntryTypeObject
	}
}

func (e EntryType) String() string {
	switch e {
	case EntryTypeInvalid:
		return "Invalid"
	case EntryTypeBlob:
		return "Blob"
	case EntryTypeTree:
		return "Tree"
	case EntryTypeObject:
		return "Object"
	case EntryTypeARI:
		return "ARI"
	default:
		return ""
	}
}

func StringToEntryType(str string) EntryType {
	switch str {
	case "Invalid":
		return EntryTypeInvalid
	case "Blob":
		return EntryTypeBlob
	case "Tree":
		return EntryTypeTree
	case "Object":
		return EntryTypeObject
	case "ARI":
		return EntryTypeARI
	default:
		return EntryTypeInvalid
	}
}

func (e EntryType) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.String())
}

func (e *EntryType) UnmarshalJSON(data []byte) error {
	var entryType string
	if err := json.Unmarshal(data, &entryType); err != nil {
		return err
	}
	*e = StringToEntryType(entryType)
	return nil
}

// IndexEntry is the data associated with a hash in the index.
type IndexEntry struct {
	EntryType EntryType `json:",omitempty"`

	// Content exists as a loose file.
	// We do not currently use this field,
	// it is plumbing for the future.
	Loose bool `json:",omitempty"`

	// Object is Local.
	// We do not currently use this field,
	// it is plumbing for the future.
	Local bool `json:",omitempty"`

	// Content has been pushed to remote.
	Remote bool `json:",omitempty"`

	// Content should not be pushed to remote.
	Offline bool `json:",omitempty"`

	// Size in bytes of the content, if known.
	Size uint64 `json:",omitempty"`

	// It will used for garbage collection
	LastAliveTime time.Time `json:",omitempty"`
}

func (i *IndexEntry) Equal(entry *IndexEntry) bool {
	if entry == nil {
		return false
	}

	return *i == *entry
}

func (i *IndexEntry) ShallowCopy() IndexEntry {
	return *i
}

func (i *IndexEntry) LocalSize() uint64 {
	if i == nil || !i.Local {
		return 0
	}

	return i.Size
}

func (i *IndexEntry) LocalCount() int64 {
	if i == nil || !i.Local {
		return 0
	}

	return 1
}

type IndexKeyEntry struct {
	Key entities.Hash
	*IndexEntry
}

type KeyTimestamp struct {
	Key       entities.Hash
	Timestamp int64
}

func (i *IndexKeyEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		Key string `json:",omitempty"`
		*IndexEntry
	}{
		Key:        i.Key.String(),
		IndexEntry: i.IndexEntry,
	})
}

func (i *IndexKeyEntry) UnmarshalJSON(data []byte) error {
	aux := &struct {
		Key string `json:",omitempty"`
		*IndexEntry
	}{}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	i.Key = entities.NewHash(aux.Key)
	i.IndexEntry = aux.IndexEntry

	return nil
}

type Index interface {
	Get(hash entities.Hash) *IndexEntry
	Put(hash entities.Hash, entry *IndexEntry) (newSize int64, newLRUSize int64, prevEnt, newEnt *IndexEntry, err error)
	PutMany(entries []*IndexKeyEntry) (newSize int64, newLRUSize int64, err error)
	EvictCount() int64
	LockEviction() func()
	Delete(hash entities.Hash) error
	EntriesWithTotalSize() ([]*IndexKeyEntry, uint64, error)
	TotalSize() uint64
	ReadLRU() ([]KeyTimestamp, error)
	Use(hash entities.Hash) error
	Close() error
	KeepAlive(key entities.Hash) error
	Sweep(timestampThreshold int64) (deletedCount int, err error)
	markObjectRemote(key entities.Hash) error
	markObjectOffline(key entities.Hash) error

	// Metrics and internal status getters
	LRUSize() int64
	LocalCount() int64
	IndexCount() int64
	GCStart() time.Time
	GCEnd() time.Time
	EvictStart() time.Time
	EvictEnd() time.Time
	OldestUsed() time.Time

	// Internal/store methods exposed for direct usage
	UseMany(hashes []entities.Hash) error
	DeleteMany(hashes []entities.Hash) (int64, int64, error)
	ForEach(fn func(key entities.Hash, entry *IndexEntry, expiresAt uint64) error) error
}

type EmptyIndex struct{}

func (e *EmptyIndex) Get(hash entities.Hash) *IndexEntry {
	return nil
}

func (e *EmptyIndex) Put(hash entities.Hash, entry *IndexEntry) (newSize int64, newLRUSize int64, prevEnt, newEnt *IndexEntry, err error) {
	return 0, 0, nil, nil, nil
}

func (e *EmptyIndex) PutMany(entries []*IndexKeyEntry) (newSize int64, newLRUSize int64, err error) {
	return 0, 0, nil
}

func (e *EmptyIndex) EvictCount() int64 {
	return 0
}

func (e *EmptyIndex) LockEviction() func() {
	return func() {}
}

func (e *EmptyIndex) Delete(hash entities.Hash) error {
	return nil
}

func (e *EmptyIndex) EntriesWithTotalSize() ([]*IndexKeyEntry, uint64, error) {
	return nil, 0, nil
}

func (e *EmptyIndex) TotalSize() uint64 {
	return 0
}

func (e *EmptyIndex) ReadLRU() ([]KeyTimestamp, error) {
	return nil, nil
}

func (e *EmptyIndex) Use(hash entities.Hash) error {
	return nil
}

func (e *EmptyIndex) Close() error {
	return nil
}

func (e *EmptyIndex) KeepAlive(key entities.Hash) error {
	return nil
}

func (e *EmptyIndex) Sweep(timestampThreshold int64) (deletedCount int, err error) {
	return 0, nil
}

func (e *EmptyIndex) markObjectRemote(key entities.Hash) error {
	return nil
}

func (e *EmptyIndex) markObjectOffline(key entities.Hash) error {
	return nil
}

func (e *EmptyIndex) LRUSize() int64 {
	return 0
}

func (e *EmptyIndex) LocalCount() int64 {
	return 0
}

func (e *EmptyIndex) IndexCount() int64 {
	return 0
}

func (e *EmptyIndex) GCStart() time.Time {
	return time.Time{}
}

func (e *EmptyIndex) GCEnd() time.Time {
	return time.Time{}
}

func (e *EmptyIndex) EvictStart() time.Time {
	return time.Time{}
}

func (e *EmptyIndex) EvictEnd() time.Time {
	return time.Time{}
}

func (e *EmptyIndex) OldestUsed() time.Time {
	return time.Time{}
}

func (e *EmptyIndex) UseMany(hashes []entities.Hash) error {
	return nil
}

func (e *EmptyIndex) DeleteMany(hashes []entities.Hash) (int64, int64, error) {
	return 0, 0, nil
}

func (e *EmptyIndex) ForEach(fn func(key entities.Hash, entry *IndexEntry, expiresAt uint64) error) error {
	return nil
}

type badgerIndex struct {
	printer Printer
	logDir  string

	indexStore IndexStore

	/* Eviction Related Fields */
	cache *localcache.LocalCache

	// Protects indexStore DB mutations against BadgerDB transaction conflicts with batch eviction:
	// Evict() and Sweep() take Lock(); concurrent index mutators (Put, Delete, Use, etc.) take RLock().
	evictRWLock sync.RWMutex

	// 1-buffered token lock serializing full eviction lifecycles (DB eviction + on-disk unlinking in
	// evictIndexAndCache and Sweep) against LockEviction() holders (maintenanceWork, clean) and Close().
	evictCh chan struct{}

	// This is a soft threshold and the point when eviction actions are started.
	// Writers are not prevented from writing to disk. This is a performance gain
	// at the expense of temporarily exceeding size threshold.
	evictThreshold uint64

	evictStart time.Time
	evictEnd   time.Time
	evictCount atomic.Int64
	oldestUsed time.Time
	closed     atomic.Bool
}

type IndexOptions struct {
	IndexStore     IndexStore
	Cache          *localcache.LocalCache
	EvictThreshold uint64
	Printer        Printer
	LogDir         string
}

func NewIndex(opts *IndexOptions) (Index, error) {
	index := &badgerIndex{
		indexStore:     opts.IndexStore,
		cache:          opts.Cache,
		evictCh:        make(chan struct{}, 1),
		evictThreshold: opts.EvictThreshold,
		printer:        PrinterOrDefault(opts.Printer),
		logDir:         opts.LogDir,
	}
	if err := index.indexStore.Open(); err != nil {
		return nil, err
	}
	index.updateOldestObjectStats()
	return index, nil
}

// Get returns the index entry associated with the hash.
// Read-Only transaction doesn't need the evict lock since it won't cause conflict.
func (i *badgerIndex) Get(hash entities.Hash) *IndexEntry {
	return i.indexStore.Get(hash)
}

// Put adds an index entry to the index, and starts the eviction process
// if needed.
func (i *badgerIndex) Put(hash entities.Hash, entry *IndexEntry) (newSize int64, newLRUSize int64, prevEnt, newEnt *IndexEntry, err error) {
	i.evictRWLock.RLock()
	newSize, newLRUSize, prevEnt, newEnt, err = i.indexStore.Put(hash, entry)
	i.evictRWLock.RUnlock()
	if err != nil {
		return 0, 0, nil, nil, err
	}
	if indexDebug {
		i.printer.Printf("Put(%v) transaction committed\n", hash)
	}

	i.maybeTriggerEviction(newLRUSize)
	return
}

// PutMany adds multiple index entries to the index in a batch, and starts the eviction process
// if needed.
func (i *badgerIndex) PutMany(entries []*IndexKeyEntry) (newSize int64, newLRUSize int64, err error) {
	if len(entries) == 0 {
		return i.indexStore.TotalSize(), i.indexStore.LRUSize(), nil
	}

	i.evictRWLock.RLock()
	newSize, newLRUSize, err = i.indexStore.PutMany(entries)
	i.evictRWLock.RUnlock()
	if err != nil {
		return 0, 0, err
	}
	if indexDebug {
		i.printer.Printf("PutMany(%d entries) transaction committed\n", len(entries))
	}

	i.maybeTriggerEviction(newLRUSize)
	return newSize, newLRUSize, nil
}

func (i *badgerIndex) maybeTriggerEviction(newLRUSize int64) {
	if i.evictThreshold == 0 || i.closed.Load() {
		return
	}

	bgThreshold := int64(float64(i.evictThreshold) * evictBackgroundTrigger)
	if newLRUSize <= bgThreshold {
		return
	}

	// Proactive: threshold reached -> non-blocking background eviction
	select {
	case i.evictCh <- struct{}{}:
		if i.closed.Load() {
			<-i.evictCh
			return
		}
		go func() {
			defer func() { <-i.evictCh }()
			if err := i.evictIndexAndCache(); err != nil {
				i.printer.Printf("Background eviction failed: %v\n", err)
			}
		}()
	default:
		// Fall through - an eviction method is already active
	}
}

func (i *badgerIndex) EvictCount() int64 {
	return i.evictCount.Load()
}

// LockEviction acquires the exclusive eviction token, waiting for any in-progress
// eviction to complete and preventing new background evictions from starting until
// the returned unlock function is called. On unlock, it re-evaluates whether
// eviction needs to be triggered.
func (i *badgerIndex) LockEviction() func() {
	i.evictCh <- struct{}{}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-i.evictCh
			i.maybeTriggerEviction(i.indexStore.LRUSize())
		})
	}
}

// evictIndexAndCache removes index entries and localcache objects
// until sufficient space is available. The metadata is purged prior
// to disk contents.
func (i *badgerIndex) evictIndexAndCache() error {
	i.evictStart = time.Now()
	startLRUSz := i.indexStore.LRUSize()
	currLRUSz := startLRUSz
	i.printer.Printf("Starting eviction process with starting LRU size %s...\n", humanize.Bytes(uint64(currLRUSz)))

	var allVictims []*IndexKeyEntry

	defer func() {
		i.evictEnd = time.Now()
		i.printer.Printf("Eviction process completed in %v with ending LRU size %s\n", i.evictEnd.Sub(i.evictStart), humanize.Bytes(uint64(currLRUSz)))
		i.updateOldestObjectStats()
		i.writeEvictionLog(startLRUSz, currLRUSz, allVictims)
	}()

	targetSize := int64(float64(i.evictThreshold) * (1.0 - evictHeadroom))

	for currLRUSz > targetSize {
		// Evict from the index store while holding the lock for DB metadata updates.
		// It is possible that we are interrupted in between evicting
		// a cached item from the index, but before removing it from disk.
		// This will get rectified on a cacheman maintenance run.
		i.evictRWLock.Lock()
		victims, _, lruSz, err := i.indexStore.Evict()
		i.evictRWLock.Unlock()
		if err != nil {
			return err
		}
		currLRUSz = lruSz

		i.evictCount.Add(int64(len(victims)))
		allVictims = append(allVictims, victims...)

		eg := new(errgroup.Group)
		eg.SetLimit(runtime.NumCPU())

		// Evict from localcache without holding evictRWLock.
		for _, victim := range victims {
			if indexDebug {
				i.printer.Printf("Evict victim is %v\n", victim.Key)
			}
			eg.Go(func() error {
				if !victim.Remote {
					i.printer.Printf("WARNING: eviction victim %v was not pushed to the remote. The eviction threshold may be too low.\n", victim.Key)
				}

				return i.removeCachedObject(victim)
			})
		}
		if err := eg.Wait(); err != nil {
			return err
		}
	}

	return nil
}

// writeEvictionLog records a dedicated, atomic eviction log (recent_eviction.log) containing the
// metadata and all evicted object hashes from the most recent eviction run.
func (i *badgerIndex) writeEvictionLog(startLRUSz, endLRUSz int64, victims []*IndexKeyEntry) {
	if i.logDir == "" || len(victims) == 0 {
		return
	}

	targetLogPath := filepath.Join(i.logDir, "recent_eviction.log")
	tmpLogPath := filepath.Join(i.logDir, fmt.Sprintf("recent_eviction.log.tmp.%d", time.Now().UnixNano()))

	f, err := os.Create(tmpLogPath)
	if err != nil {
		i.printer.Printf("failed to create eviction log tempfile %s: %v\n", tmpLogPath, err)
		return
	}

	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "# Eviction Run Summary\n")
	fmt.Fprintf(w, "Start: %s\n", i.evictStart.UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(w, "End: %s\n", i.evictEnd.UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(w, "Duration: %s\n", i.evictEnd.Sub(i.evictStart))
	fmt.Fprintf(w, "StartingLRUSize: %s (%d bytes)\n", humanize.Bytes(uint64(startLRUSz)), startLRUSz)
	fmt.Fprintf(w, "EndingLRUSize: %s (%d bytes)\n", humanize.Bytes(uint64(endLRUSz)), endLRUSz)
	targetSize := int64(float64(i.evictThreshold) * (1.0 - evictHeadroom))
	fmt.Fprintf(w, "TargetLRUSize: %s (%d bytes)\n", humanize.Bytes(uint64(targetSize)), targetSize)
	fmt.Fprintf(w, "TotalEvicted: %d\n\n", len(victims))
	fmt.Fprintf(w, "# Format: <hash> <type> <size_bytes>\n")

	for _, v := range victims {
		if v == nil {
			continue
		}
		var sz uint64
		if v.IndexEntry != nil {
			sz = v.Size
		}
		fmt.Fprintf(w, "%s %s %d\n", v.Key.String(), strings.ToLower(v.EntryType.String()), sz)
	}

	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmpLogPath)
		i.printer.Printf("failed to flush eviction log: %v\n", err)
		return
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpLogPath)
		i.printer.Printf("failed to close eviction log tempfile: %v\n", err)
		return
	}

	if err := os.Rename(tmpLogPath, targetLogPath); err != nil {
		os.Remove(tmpLogPath)
		i.printer.Printf("failed to rename eviction log from %s to %s: %v\n", tmpLogPath, targetLogPath, err)
	}
}

func (i *badgerIndex) updateOldestObjectStats() {
	tk, err := i.indexStore.PeekOldest()
	if err != nil {
		i.printer.Printf("failed to peek the oldest object in the cache: %v\n", err)
	} else {
		i.oldestUsed = time.Unix(tk.Timestamp, 0)
	}
}

func filterNoExist(e error) error {
	if entities.IsNotExist(e) {
		return nil
	}

	return e
}

func (i *badgerIndex) removeCachedObject(entry *IndexKeyEntry) error {
	// Allow this method to accept missing entries or missing cache.
	if entry == nil || i.cache == nil {
		return nil
	}

	// Note: evictRWLock is released before disk removal. This is safe without
	// holding the lock or checking for re-fetch due to two invariants:
	//
	// 1. In-flight reads (Pre-unlink): If CASFS is actively reading this file, it holds
	//    an open kernel file reference (priv->real_file), so unlinking the directory entry
	//    will not invalidate in-flight reads under POSIX semantics.
	// 2. Re-fetch causality (Post-unlink): CASFS only issues a fetch request after
	//    physically seeing ENOENT on disk. Therefore, a fetch for this hash can only be
	//    initiated AFTER this unlink has fully completed. Since the hash was already purged
	//    from the LRU during indexStore.Evict(), it cannot be selected as an eviction
	//    victim again until re-indexed and re-added to LRU.

	ctx := context.Background()
	key := entry.Key

	var err error
	switch entry.EntryType {
	case EntryTypeBlob:
		err = i.cache.RemoveBlob(ctx, key)
	case EntryTypeObject:
		err = i.cache.RemoveObject(ctx, key)
	case EntryTypeTree:
		err = i.cache.RemoveObject(ctx, key)
		if filterNoExist(err) != nil {
			return err
		}
		err = i.cache.RemoveMeta(ctx, key, localcache.TreeMetaTag)
	case EntryTypeARI:
		err = i.cache.RemoveARI(ctx, key)
	default:
	}

	// Tolerate the object not existing.
	return filterNoExist(err)
}

func (i *badgerIndex) markObjectRemote(key entities.Hash) error {
	i.evictRWLock.RLock()
	defer i.evictRWLock.RUnlock()

	return i.indexStore.MarkObjectRemote(key)
}

func (i *badgerIndex) markObjectOffline(key entities.Hash) error {
	i.evictRWLock.RLock()
	defer i.evictRWLock.RUnlock()

	return i.indexStore.MarkObjectOffline(key)
}

// Delete will remove the index entry and update the
// size information. Non-existing keys are gracefully ignored.
func (i *badgerIndex) Delete(hash entities.Hash) error {
	i.evictRWLock.RLock()
	defer i.evictRWLock.RUnlock()

	_, _, _, err := i.indexStore.Delete(hash)

	return err
}

// Read-Only transaction doesn't need the evict lock since it won't cause conflict.
func (i *badgerIndex) EntriesWithTotalSize() ([]*IndexKeyEntry, uint64, error) {
	entries, err := i.indexStore.ReadAll()
	if err != nil {
		return nil, 0, err
	}

	return entries, uint64(i.indexStore.TotalSize()), nil
}

func (i *badgerIndex) TotalSize() uint64 {
	return uint64(i.indexStore.TotalSize())
}

func (i *badgerIndex) ReadLRU() ([]KeyTimestamp, error) {
	return i.indexStore.ReadLRU()
}

func (i *badgerIndex) Use(hash entities.Hash) error {
	i.evictRWLock.RLock()
	defer i.evictRWLock.RUnlock()

	return i.indexStore.Use(hash)
}

func (i *badgerIndex) Close() error {
	i.closed.Store(true)
	i.evictCh <- struct{}{}
	defer func() { <-i.evictCh }()
	return i.indexStore.Close()
}

func (i *badgerIndex) KeepAlive(key entities.Hash) error {
	return i.indexStore.KeepAlive(key)
}

func (i *badgerIndex) Sweep(timestampThreshold int64) (deletedCount int, err error) {
	i.evictCh <- struct{}{}
	defer func() { <-i.evictCh }()
	if i.closed.Load() {
		return 0, nil
	}

	// Backpressure put() calls during sweep.
	i.evictRWLock.Lock()
	defer i.evictRWLock.Unlock()

	t := time.Now()
	entries, err := i.indexStore.ReadAll()
	if err != nil {
		return 0, err
	}
	i.printer.Println("Starting Sweep process...")
	defer func() {
		i.printer.Println("Sweep process took", time.Since(t))
	}()
	var toDeleteKeys []entities.Hash
	var toDeleteEntries []*IndexKeyEntry
	for _, keyEntry := range entries {
		// We only sweep unreachable blobs
		entry := keyEntry.IndexEntry
		if entry.EntryType != EntryTypeBlob {
			continue
		}
		if entry.Local && !entry.Remote && entry.LastAliveTime.Unix() < timestampThreshold {
			toDeleteKeys = append(toDeleteKeys, keyEntry.Key)
			toDeleteEntries = append(toDeleteEntries, keyEntry)
		}
	}

	if len(toDeleteKeys) == 0 {
		return 0, nil
	}
	cnt := i.indexStore.IndexCount()
	_, _, err = i.indexStore.DeleteMany(toDeleteKeys)

	if err != nil {
		return 0, err
	}

	eg := new(errgroup.Group)
	eg.SetLimit(runtime.NumCPU())

	// Evict from localcache.
	// TODO: prevent race condition for removeCachedObject
	for _, entry := range toDeleteEntries {
		eg.Go(func() error {
			return i.removeCachedObject(entry)
		})
	}
	if err := eg.Wait(); err != nil {
		return 0, err
	}

	afterCnt := i.indexStore.IndexCount()

	swept := int(cnt - afterCnt)
	i.evictCount.Add(int64(swept))

	return swept, nil
}

func (i *badgerIndex) LRUSize() int64 {
	return i.indexStore.LRUSize()
}

func (i *badgerIndex) LocalCount() int64 {
	return i.indexStore.LocalCount()
}

func (i *badgerIndex) IndexCount() int64 {
	return i.indexStore.IndexCount()
}

func (i *badgerIndex) GCStart() time.Time {
	return i.indexStore.GCStart()
}

func (i *badgerIndex) GCEnd() time.Time {
	return i.indexStore.GCEnd()
}

func (i *badgerIndex) EvictStart() time.Time {
	return i.evictStart
}

func (i *badgerIndex) EvictEnd() time.Time {
	return i.evictEnd
}

func (i *badgerIndex) OldestUsed() time.Time {
	return i.oldestUsed
}

func (i *badgerIndex) UseMany(hashes []entities.Hash) error {
	i.evictRWLock.RLock()
	defer i.evictRWLock.RUnlock()

	return i.indexStore.UseMany(hashes)
}

func (i *badgerIndex) DeleteMany(hashes []entities.Hash) (int64, int64, error) {
	i.evictRWLock.RLock()
	defer i.evictRWLock.RUnlock()

	return i.indexStore.DeleteMany(hashes)
}

func (i *badgerIndex) ForEach(fn func(key entities.Hash, entry *IndexEntry, expiresAt uint64) error) error {
	return i.indexStore.ForEach(fn)
}
