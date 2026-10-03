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

package rbe

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/rbe/model"
)

func TestBadgerIdentityStore(t *testing.T) {
	tempDir := t.TempDir()
	store, err := NewBadgerIdentityStore(filepath.Join(tempDir, "identity"))
	require.NoError(t, err)
	defer store.Close()

	hash1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	node1 := model.Node{
		Digest: model.Digest{
			SHA256: "sha256-1111",
			Size:   100,
		},
		Type: model.NodeTypeFile,
	}

	// Test single Put & Get
	err = store.Put(hash1, node1)
	require.NoError(t, err)

	gotNode, found, err := store.Get(hash1)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, node1, gotNode)

	// Test Get not found
	hashMissing := entities.NewHash("ffffffffffffffffffffffffffffffffffffffff")
	_, found, err = store.Get(hashMissing)
	require.NoError(t, err)
	assert.False(t, found)

	// Test PutMany
	hash2 := entities.NewHash("b0423896973644771497bdc03eb99d5281615b52")
	hash3 := entities.NewHash("c0423896973644771497bdc03eb99d5281615b53")
	node2 := model.Node{
		Digest: model.Digest{
			SHA256: "sha256-2222",
			Size:   200,
		},
		Type: model.NodeTypeDirectory,
	}
	node3 := model.Node{
		Digest: model.Digest{
			SHA256: "sha256-3333",
			Size:   300,
		},
		Type: model.NodeTypeSymlink,
	}

	entries := []IdentityEntry{
		{SHA1: hash2, Node: node2},
		{SHA1: hash3, Node: node3},
	}
	err = store.PutMany(entries)
	require.NoError(t, err)

	gotNode2, found, err := store.Get(hash2)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, node2, gotNode2)

	gotNode3, found, err := store.Get(hash3)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, node3, gotNode3)
}
