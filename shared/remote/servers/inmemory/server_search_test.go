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

package inmemory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
)

func TestInMemoryServer_SearchRefs(t *testing.T) {
	ctx := context.Background()
	server := New()

	testRefs := []remote.RefHash{
		{Ref: "refs/heads/main", Hash: entities.EmptyFileHash},
		{Ref: "refs/heads/feature-a", Hash: entities.EmptyFileHash},
		{Ref: "refs/heads/feature-b", Hash: entities.EmptyFileHash},
		{Ref: "refs/heads/bugfix/x", Hash: entities.EmptyFileHash},
		{Ref: "refs/heads/bugfix/y", Hash: entities.EmptyFileHash},
	}

	err := server.PutRefs(ctx, "default-server", "test-project", testRefs)
	require.NoError(t, err)

	t.Run("Unlimited (limit=0)", func(t *testing.T) {
		res, err := server.SearchRefs(ctx, "default-server", "test-project", "refs/heads/", 0)
		require.NoError(t, err)

		// Sorted descending (reverse lexicographical)
		// Expected: main > feature-b > feature-a > bugfix-y > bugfix-x
		expected := []string{
			"refs/heads/main",
			"refs/heads/feature-b",
			"refs/heads/feature-a",
			"refs/heads/bugfix/y",
			"refs/heads/bugfix/x",
		}

		require.Len(t, res, len(expected))
		for i, refName := range expected {
			assert.Equal(t, refName, res[i].Ref)
		}
	})

	t.Run("Limited (limit=2)", func(t *testing.T) {
		res, err := server.SearchRefs(ctx, "default-server", "test-project", "refs/heads/", 2)
		require.NoError(t, err)

		expected := []string{
			"refs/heads/main",
			"refs/heads/feature-b",
		}

		require.Len(t, res, len(expected))
		for i, refName := range expected {
			assert.Equal(t, refName, res[i].Ref)
		}
	})

	t.Run("Limited Prefix Match (limit=1)", func(t *testing.T) {
		res, err := server.SearchRefs(ctx, "default-server", "test-project", "refs/heads/bugfix", 1)
		require.NoError(t, err)

		expected := []string{
			"refs/heads/bugfix/y",
		}

		require.Len(t, res, len(expected))
		for i, refName := range expected {
			assert.Equal(t, refName, res[i].Ref)
		}
	})

	t.Run("Limit Exceeding Count (limit=10)", func(t *testing.T) {
		res, err := server.SearchRefs(ctx, "default-server", "test-project", "refs/heads/", 10)
		require.NoError(t, err)

		expected := []string{
			"refs/heads/main",
			"refs/heads/feature-b",
			"refs/heads/feature-a",
			"refs/heads/bugfix/y",
			"refs/heads/bugfix/x",
		}

		require.Len(t, res, len(expected))
		for i, refName := range expected {
			assert.Equal(t, refName, res[i].Ref)
		}
	})
}

func TestInMemoryServer_XchgRefs(t *testing.T) {
	ctx := context.Background()
	server := New()

	// Empty XchgRefs is a no-op.
	require.NoError(t, server.XchgRefs(ctx, "srv", "prj", nil))

	// Chained updates on the same ref in a single batch succeed (ZeroHash -> EmptyFileHash -> EmptyDirHash).
	err := server.XchgRefs(ctx, "srv", "prj", []remote.RefXchg{
		{Ref: "refs/heads/main", Current: entities.ZeroHash, Desired: entities.EmptyFileHash},
		{Ref: "refs/heads/main", Current: entities.EmptyFileHash, Desired: entities.EmptyDirHash},
	})
	require.NoError(t, err)

	got, err := server.GetRefs(ctx, "srv", "prj", []string{"refs/heads/main"})
	require.NoError(t, err)
	require.Equal(t, []remote.Hash{entities.EmptyDirHash}, got)

	// Unchained duplicate ref in a batch fails and does not mutate existing refs.
	err = server.XchgRefs(ctx, "srv", "prj", []remote.RefXchg{
		{Ref: "refs/heads/main", Current: entities.EmptyDirHash, Desired: entities.EmptyFileHash},
		{Ref: "refs/heads/main", Current: entities.EmptyDirHash, Desired: entities.ZeroHash},
	})
	var mismatchErr *remote.ErrRefMismatch
	require.ErrorAs(t, err, &mismatchErr)
	require.Equal(t, "refs/heads/main", mismatchErr.Ref)
	require.Equal(t, entities.EmptyDirHash, mismatchErr.Current)
	require.Equal(t, entities.EmptyFileHash, mismatchErr.Actual)

	got, err = server.GetRefs(ctx, "srv", "prj", []string{"refs/heads/main"})
	require.NoError(t, err)
	require.Equal(t, []remote.Hash{entities.EmptyDirHash}, got)
}
