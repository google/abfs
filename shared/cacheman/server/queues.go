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
	"container/heap"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gammazero/deque"

	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/entities"
)

type FetchReq struct {
	Hash      entities.Hash
	ARI       string
	SHA256Hex string
	Size      int64
	Priority  float64
	Time      time.Time
	ObjType   protocol.RequestObjectType
	callback  func(error)
}

type fetchReqHeap []FetchReq

func (h fetchReqHeap) Len() int { return len(h) }
func (h fetchReqHeap) Less(i, j int) bool {
	if h[i].Priority != h[j].Priority {
		return h[i].Priority > h[j].Priority // Higher priority first
	}
	return h[i].Time.Before(h[j].Time) // Earlier time first
}
func (h fetchReqHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}
func (h *fetchReqHeap) Push(x any) {
	*h = append(*h, x.(FetchReq))
}
func (h *fetchReqHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

type FetchReqQueue struct {
	cond   *sync.Cond
	mu     sync.Mutex
	heap   fetchReqHeap
	closed bool
}

func NewFetchReqQueue() *FetchReqQueue {
	q := &FetchReqQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *FetchReqQueue) Push(req FetchReq) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if req.Time.IsZero() {
		req.Time = time.Now()
	}
	heap.Push(&q.heap, req)
	q.cond.Signal()
}

func (q *FetchReqQueue) Pop() (FetchReq, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.heap.Len() == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed && q.heap.Len() == 0 {
		return FetchReq{}, false
	}
	return heap.Pop(&q.heap).(FetchReq), true
}

func (q *FetchReqQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

type relativeFetchStat struct {
	RemainDepth  int
	RemainCntItr *atomic.Int32
}

func (r *relativeFetchStat) Next() (relativeFetchStat, bool) {
	if cnt := r.RemainCntItr.Add(-1); cnt < 0 {
		return relativeFetchStat{}, false
	} else if r.RemainDepth <= 0 {
		return relativeFetchStat{}, false
	}

	return relativeFetchStat{
		RemainDepth:  r.RemainDepth - 1,
		RemainCntItr: r.RemainCntItr,
	}, true
}

type RelativeFetchReq struct {
	FetchReq
	FetchStat   *relativeFetchStat
	RequestType entities.ObjectType
}

type IndexReq struct {
	Hash      entities.Hash
	EntryType EntryType
	ObjType   protocol.RequestObjectType
	ReqStat   *protocol.QueueStat
	RawType   string
	// Size carries the known on-disk size computed during fetch to avoid redundant disk I/O in indexObjectsWorker.
	Size int64
}

// BlockingQueue is an unbounded thread-safe queue.
type BlockingQueue[T any] struct {
	cond   *sync.Cond
	mu     sync.Mutex
	queue  deque.Deque[T]
	closed bool
}

func NewBlockingQueue[T any]() *BlockingQueue[T] {
	q := &BlockingQueue[T]{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *BlockingQueue[T]) Push(item T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queue.PushBack(item)
	q.cond.Signal()
}

func (q *BlockingQueue[T]) Pop() (T, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.queue.Len() == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed && q.queue.Len() == 0 {
		var zero T
		return zero, false
	}
	return q.queue.PopFront(), true
}

func (q *BlockingQueue[T]) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

// IndexReqQueue is an unbounded dynamic queue for offloading index registration
// after fetched objects have been written to disk.
type IndexReqQueue = BlockingQueue[IndexReq]

func NewIndexReqQueue() *IndexReqQueue {
	return NewBlockingQueue[IndexReq]()
}

type NewObjectReq struct {
	Hash    entities.Hash
	ObjType protocol.RequestObjectType
}

// NewObjectReqQueue is an unbounded dynamic queue for offloading local object
// index registration and remote eager-push after new objects are written by CASFS.
// It deduplicates pending requests in-memory to prevent duplicate hash conflicts.
type NewObjectReqQueue struct {
	cond    *sync.Cond
	mu      sync.Mutex
	queue   deque.Deque[NewObjectReq]
	inQueue map[entities.Hash]struct{}
	closed  bool
}

func NewNewObjectReqQueue() *NewObjectReqQueue {
	q := &NewObjectReqQueue{
		inQueue: make(map[entities.Hash]struct{}),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *NewObjectReqQueue) Push(req NewObjectReq) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if _, exists := q.inQueue[req.Hash]; exists {
		return
	}
	q.inQueue[req.Hash] = struct{}{}
	q.queue.PushBack(req)
	q.cond.Signal()
}

func (q *NewObjectReqQueue) Pop() (NewObjectReq, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.queue.Len() == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.queue.Len() == 0 {
		return NewObjectReq{}, false
	}
	req := q.queue.PopFront()
	delete(q.inQueue, req.Hash)
	return req, true
}

func (q *NewObjectReqQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.queue.Len()
}

func (q *NewObjectReqQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

// NotifyUseAggregator coalesces and deduplicates high-frequency LRU touch notifications
// in-memory to prevent BadgerDB transaction thrashing on the socket connection loop.
type NotifyUseAggregator struct {
	mu     sync.Mutex
	hashes map[entities.Hash]struct{}
}

func NewNotifyUseAggregator() *NotifyUseAggregator {
	return &NotifyUseAggregator{
		hashes: make(map[entities.Hash]struct{}),
	}
}

func (a *NotifyUseAggregator) Add(hash entities.Hash) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hashes[hash] = struct{}{}
}

func (a *NotifyUseAggregator) Flush() []entities.Hash {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.hashes) == 0 {
		return nil
	}
	res := make([]entities.Hash, 0, len(a.hashes))
	for h := range a.hashes {
		res = append(res, h)
	}
	clear(a.hashes)
	return res
}

// PendingAddTracker tracks in-flight fetched objects that have been written to disk
// but are still awaiting asynchronous index registration.
//
// Operations on this tracker are purely in-memory (no file stating, no disk I/O,
// and no BadgerDB transaction conflicts). It is therefore low latency to call
// synchronously on the fetch response path before unblocking CASFS.
//
// By recording hashes here synchronously before performCallbacks runs, we eliminate
// the race window where a process reading the fetched file immediately writes it to a
// CASFS mount and triggers NewObject before indexObjectsWorker has committed the index entry.
type PendingAddTracker struct {
	mu     sync.Mutex
	counts map[entities.Hash]int
}

func NewPendingAddTracker() *PendingAddTracker {
	return &PendingAddTracker{
		counts: make(map[entities.Hash]int),
	}
}

func (p *PendingAddTracker) Add(hash entities.Hash) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counts[hash]++
}

func (p *PendingAddTracker) Remove(hash entities.Hash) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.counts[hash] <= 1 {
		delete(p.counts, hash)
	} else {
		p.counts[hash]--
	}
}

func (p *PendingAddTracker) Has(hash entities.Hash) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.counts[hash]
	return ok
}
