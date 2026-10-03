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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPriorityQueue_Empty(t *testing.T) {
	pq := NewPriorityQueue()

	require.Equal(t, 0, pq.Len())

	require.Nil(t, pq.Peek())
}

func TestPriorityQueue(t *testing.T) {
	pq := NewPriorityQueue()

	node1 := &node{priority: QueuePriority{priority: 10}}
	node2 := &node{priority: QueuePriority{priority: 15}}
	node3 := &node{priority: QueuePriority{priority: 5}}

	pq.Push(node1)
	pq.Push(node2)
	pq.Push(node3)

	require.Equal(t, 3, pq.Len())

	require.Equal(t, node2, pq.Peek())

	expectedOrder := []*node{node2, node1, node3}

	for _, expected := range expectedOrder {
		item := pq.Peek()
		require.Equal(t, expected, item)
		pq.Remove(item)
	}

	require.Equal(t, 0, pq.Len())
}

func TestPriorityQueue_Update(t *testing.T) {
	pq := NewPriorityQueue()

	node1 := &node{priority: QueuePriority{priority: 10}}
	node2 := &node{priority: QueuePriority{priority: 15}}
	node3 := &node{priority: QueuePriority{priority: 5}}

	pq.Push(node1)
	pq.Push(node2)
	pq.Push(node3)

	pq.Update(node3, 20)

	require.Equal(t, node3, pq.Peek())

	require.Equal(t, 3, pq.Len())
}
