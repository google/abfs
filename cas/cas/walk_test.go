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

package cas

import (
	"fmt"
	"testing"

	"github.com/google/abfs/cas/hash"
	"github.com/stretchr/testify/require"
)

// Walk hash corresponding to deep dir created with testCreateDeepDir
func walkDeepDir(t *testing.T, h hash.Hash, store Store, depth int) {
	count := 0
	fn := func(dh hash.Hash, de DirEntry) error {
		count++
		require.Equal(t, de.Name, fmt.Sprintf("target-%d", count))
		return nil
	}
	TreeWalk(h, store, fn)
}

// Walk deeptree hash corresponding to deep dir created with testCreateDeepDir
func walkDeepTree(t *testing.T, h hash.Hash, store Store, depth int) {
	count := 0
	fn := func(dirHash hash.Hash, de DirEntry) error {
		count++
		require.Equal(t, de.Name, fmt.Sprintf("target-%d", count))
		return nil
	}
	DeepTreeWalk(h, store, fn)
}

func TestTree_Walk(t *testing.T) {
	ts := newTestStore()
	sz := 1024
	h, err := testCreateDeepDir(ts, sz)
	require.NoError(t, err)
	walkDeepDir(t, h, ts, sz)
}
