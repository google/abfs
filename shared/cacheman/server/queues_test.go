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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func popHashes(t *testing.T, q *FetchReqQueue, n int) []entities.Hash {
	t.Helper()
	var hashes []entities.Hash
	for range n {
		req, ok := q.Pop()
		require.True(t, ok)
		hashes = append(hashes, req.Hash)
	}
	return hashes
}

func TestFetchReqQueue_Raise(t *testing.T) {
	a, b, c := hashOf("a"), hashOf("b"), hashOf("c")

	tests := []struct {
		name     string
		raise    entities.Hash
		priority float64
		raised   bool
		want     []entities.Hash
	}{
		{"higher", c, 1.0, true, []entities.Hash{c, a, b}},
		{"equal", c, 0.3, false, []entities.Hash{a, b, c}},
		{"lower", c, 0.1, false, []entities.Hash{a, b, c}},
		{"not queued", hashOf("d"), 1.0, false, []entities.Hash{a, b, c}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := NewFetchReqQueue()
			for _, h := range []entities.Hash{a, b, c} {
				q.Push(FetchReq{Hash: h, Priority: 0.3})
			}
			require.Equal(t, tt.raised, q.Raise(tt.raise, tt.priority))
			require.Equal(t, tt.want, popHashes(t, q, 3))
			require.Empty(t, q.queued)
		})
	}
}

func TestFetchReqQueue_RaiseKeepsTime(t *testing.T) {
	q := NewFetchReqQueue()
	a, b := hashOf("a"), hashOf("b")
	q.Push(FetchReq{Hash: a, Priority: 0.3})
	q.Push(FetchReq{Hash: b, Priority: 1.0})
	require.True(t, q.Raise(a, 1.0))

	// a was queued first, so it sorts before b at the same priority.
	require.Equal(t, []entities.Hash{a, b}, popHashes(t, q, 2))
}

func TestFetchReqQueue_RaiseAfterPop(t *testing.T) {
	q := NewFetchReqQueue()
	a := hashOf("a")
	q.Push(FetchReq{Hash: a, Priority: 0.3})
	popHashes(t, q, 1)
	require.False(t, q.Raise(a, 1.0))
}

func TestFetchReqQueue_DuplicatePush(t *testing.T) {
	q := NewFetchReqQueue()
	a, b := hashOf("a"), hashOf("b")
	q.Push(FetchReq{Hash: a, Priority: 0.3})
	q.Push(FetchReq{Hash: b, Priority: 0.5})
	q.Push(FetchReq{Hash: a, Priority: 0.3})

	// Raise moves the first entry for a only.
	require.True(t, q.Raise(a, 1.0))
	require.Equal(t, []entities.Hash{a, b, a}, popHashes(t, q, 3))
	require.Empty(t, q.queued)
	require.False(t, q.Raise(a, 1.0))
}
