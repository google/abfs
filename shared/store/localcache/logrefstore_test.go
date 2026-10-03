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

package localcache

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/google/abfs/shared/entities"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/slices"
)

const manifestProject = "platform/manifest"

func TestRefStore(t *testing.T) {
	tmpDir, err := os.MkdirTemp(os.TempDir(), "refStore-test-*")
	if err != nil {
		t.Fatalf("cannot create temp-dir: %v", err)
	}
	defer func() {
		require.NoError(t, os.RemoveAll(tmpDir))
	}()

	store, err := NewLogRefStore(tmpDir)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	err = store.Put("example-server", manifestProject, []entities.RefHash{
		{Ref: "heads/main", Hash: hash1},
		{Ref: "changes/83/1183/1", Hash: hash2},
		{Ref: "changes/83/1183/2", Hash: hash3},
	})
	require.NoError(t, err)
	refs, err := store.Get("example-server", manifestProject, []string{"changes/83/1183/1"})
	require.NoError(t, err)
	assert.Equal(t, []entities.Hash{hash2}, refs)
	_, err = store.Get("not-existing-server", manifestProject, []string{"changes/83/1183/x"})
	require.Error(t, err)
	refs, err = store.Get("example-server", "different/project", []string{"changes/83/1183/1"})
	require.NoError(t, err)
	assert.Equal(t, []entities.Hash{entities.ZeroHash}, refs)
	err = store.Put("example-server", manifestProject, []entities.RefHash{{Ref: "changes/83/1183/1", Hash: hash4}})
	require.NoError(t, err)
	refs, err = store.Get("example-server", manifestProject, []string{"changes/83/1183/1"})
	assert.Equal(t, []entities.Hash{hash4}, refs)
	require.NoError(t, err)

	store2, err := NewLogRefStore(tmpDir)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store2.Close())
	}()

	err = store2.Put("example-server", manifestProject, []entities.RefHash{{Ref: "heads", Hash: hash1}})
	require.ErrorIs(t, err, syscall.EISDIR)
	refs, err = store2.Get("example-server", manifestProject, []string{"changes/83/1183/1"})
	require.NoError(t, err)
	assert.Equal(t, []entities.Hash{hash4}, refs)
	refs, err = store2.Get("example-server", "different/project", []string{"changes/83/1183/1"})
	assert.Equal(t, []entities.Hash{entities.ZeroHash}, refs)
	require.NoError(t, err)

	err = store2.Put("example-server", "platform/art", []entities.RefHash{{Ref: "heads/main", Hash: hash5}})
	require.NoError(t, err)

	sort := func(list []entities.ProjectRefHash) []entities.ProjectRefHash {
		slices.SortFunc(list, func(a, b entities.ProjectRefHash) int {
			return strings.Compare(a.Project+"/"+a.Ref, b.Project+"/"+b.Ref)
		})
		return list
	}
	refHashes, err := store2.Search("example-server", "", "")
	require.NoError(t, err)
	assert.Equal(t, []entities.ProjectRefHash{
		{Project: "platform/art", Ref: "heads/main", Hash: hash5},
		{Project: manifestProject, Ref: "changes/83/1183/1", Hash: hash4},
		{Project: manifestProject, Ref: "changes/83/1183/2", Hash: hash3},
		{Project: manifestProject, Ref: "heads/main", Hash: hash1},
	}, sort(refHashes))
	refHashes, err = store2.Search("example-server", "", "heads/main")
	require.NoError(t, err)
	assert.Equal(t, []entities.ProjectRefHash{
		{Project: "platform/art", Ref: "heads/main", Hash: hash5},
		{Project: manifestProject, Ref: "heads/main", Hash: hash1},
	}, sort(refHashes))
	refHashes, err = store2.Search("example-server", "", "changes/83/1183")
	require.NoError(t, err)
	assert.Equal(t, []entities.ProjectRefHash{
		{Project: manifestProject, Ref: "changes/83/1183/1", Hash: hash4},
		{Project: manifestProject, Ref: "changes/83/1183/2", Hash: hash3},
	}, sort(refHashes))
	refHashes, err = store2.Search("example-server", "platform/art", "")
	require.NoError(t, err)
	assert.Equal(t, []entities.ProjectRefHash{
		{Project: "platform/art", Ref: "heads/main", Hash: hash5},
	}, sort(refHashes))
	refHashes, err = store2.Search("example-server", manifestProject, "changes/83/1183")
	require.NoError(t, err)
	assert.Equal(t, []entities.ProjectRefHash{
		{Project: manifestProject, Ref: "changes/83/1183/1", Hash: hash4},
		{Project: manifestProject, Ref: "changes/83/1183/2", Hash: hash3},
	}, sort(refHashes))
}

func TestPutRefSeparator(t *testing.T) {
	tmpDir, err := os.MkdirTemp(os.TempDir(), "refStore-test-*")
	if err != nil {
		t.Fatalf("cannot create temp-dir: %v", err)
	}
	defer func() {
		require.NoError(t, os.RemoveAll(tmpDir))
	}()

	store, err := NewLogRefStore(tmpDir)
	require.NoError(t, err)
	defer require.NoError(t, store.Close())

	err = store.Put("android/nested", "foo/bar", []entities.RefHash{
		{Ref: "heads/main", Hash: hash1},
	})

	require.NoError(t, err)
}
