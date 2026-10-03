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

package dagsync

import (
	"container/heap"
	"time"
)

type RequestPriority float64

type QueuePriority struct {
	time     time.Time
	priority RequestPriority
}

// PriorityQueue orders by greater priority value then earlier time.
type PriorityQueue struct {
	queue underlyingQueue
}

func NewPriorityQueue() *PriorityQueue {
	return new(PriorityQueue)
}

func (q *PriorityQueue) Push(item *node) {
	heap.Push(&q.queue, item)
}

func (q *PriorityQueue) Peek() *node {
	if q.queue.Len() == 0 {
		return nil
	}
	return q.queue[0]
}

func (p *PriorityQueue) Update(n *node, priority RequestPriority) {
	n.priority.priority = priority
	heap.Fix(&p.queue, n.queuedRef)
}

func (q *PriorityQueue) Remove(n *node) {
	heap.Remove(&q.queue, n.queuedRef)
}

// Len returns the number of items safely.
func (q *PriorityQueue) Len() int {
	return q.queue.Len()
}

type underlyingQueue []*node

func (pq underlyingQueue) Len() int { return len(pq) }

// Less returns the node with greater priority value then earlier time
func (pq underlyingQueue) Less(i, j int) bool {
	if pq[i].priority.priority != pq[j].priority.priority {
		return pq[i].priority.priority > pq[j].priority.priority
	}
	return pq[i].priority.time.Before(pq[j].priority.time)
}

func (pq underlyingQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].queuedRef = i
	pq[j].queuedRef = j
}

func (pq *underlyingQueue) Push(x any) {
	n := len(*pq)
	item := x.(*node)
	item.queuedRef = n
	*pq = append(*pq, item)
}

func (pq *underlyingQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.queuedRef = -1
	*pq = old[0 : n-1]
	return item
}
