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

package entities

import (
	"encoding/json"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func SlowPathDiff(a, b string) string {
	if a == "" {
		return b
	}
	r, err := filepath.Rel(path.Join(a, ".."), b)
	if err != nil || len(r) > len(b) {
		return "/" + strings.TrimPrefix(b, "/")
	}
	return r
}

func SlowPathApply(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	if b[0] == '/' {
		return b[1:]
	}
	return path.Join(a, "..", b)
}

func TestPathDiff(t *testing.T) {
	tests := []struct {
		name     string
		a        string
		b        string
		expected string
	}{
		{"empty a and b", "", "", ""},
		{"empty a", "", "x/y", "x/y"},
		{"empty b", "x/y", "", "/"},
		{"sibling", "a/b/c/d", "a/b/c/e", "e"},
		{"parent dir", "a/b/c/d", "a/b/e/f", "../e/f"},
		{"child dir", "a/b/c/d", "a/b/c/d/e", "d/e"},
		{"shorter absolute", "a/b/c/d/e/f", "a/g", "/a/g"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, SlowPathDiff(tc.a, tc.b))
			assert.Equal(t, tc.expected, PathDiff(tc.a, tc.b))
		})
	}
}

func TestPathApply(t *testing.T) {
	tests := []struct {
		name     string
		a        string
		b        string
		expected string
	}{
		{"empty a and b", "", "", ""},
		{"empty a", "", "x/y", "x/y"},
		{"sibling", "a/b/c/d", "e", "a/b/c/e"},
		{"child dir", "a/b/c/d", "d/e", "a/b/c/d/e"},
		{"parent dirs", "a/b/c", "../../e", "e"},
		{"absolute path", "a/b/c/d", "/d/e", "d/e"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, SlowPathApply(tc.a, tc.b))
			assert.Equal(t, tc.expected, PathApply(tc.a, tc.b))
		})
	}
}

func TestPatchMarshal(t *testing.T) {
	patch := Patch{
		{
			Path:  "out/abc",
			Mode:  FileModeRegular,
			Hash:  NewHash("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"),
			Size:  0,
			State: PatchEntryStateDep,
		},
		{
			Path:  "out/def",
			Mode:  FileModeExecutable,
			Hash:  NewHash("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"),
			Size:  1234,
			State: PatchEntryStateIn,
		},
	}

	result, err := json.Marshal(patch)
	require.NoError(t, err)
	assert.Equal(t, `[["out/abc","f","e69de29bb2d1d6434b8b29ae775ad8c2e48c5391","0","4"],["def","x","e69de29bb2d1d6434b8b29ae775ad8c2e48c5391","1234","3"]]`, string(result))
}

func TestPatchUnmarshal(t *testing.T) {
	var patch Patch
	err := json.Unmarshal(
		[]byte(`[["out/abc","f","e69de29bb2d1d6434b8b29ae775ad8c2e48c5391","0","4"],["def","x","e69de29bb2d1d6434b8b29ae775ad8c2e48c5391","1234","3"]]`),
		&patch,
	)
	require.NoError(t, err)
	assert.Equal(t, Patch{
		{
			Path:  "out/abc",
			Mode:  FileModeRegular,
			Hash:  NewHash("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"),
			Size:  0,
			State: PatchEntryStateDep,
		},
		{
			Path:  "out/def",
			Mode:  FileModeExecutable,
			Hash:  NewHash("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"),
			Size:  1234,
			State: PatchEntryStateIn,
		},
	}, patch)
}

func TestDeletePatchMarshal(t *testing.T) {
	patch := DeletePatch{
		"",
		"a/b/c/d",
		"a/b/c/d/e",
		"a/b/c/d/f",
		"g",
		"g/h/i",
	}

	result, err := json.Marshal(patch)
	require.NoError(t, err)
	assert.Equal(t, `["","a/b/c/d","d/e","f","/g","g/h/i"]`, string(result))
}

func TestDeletePatchUnmarshal(t *testing.T) {
	var patch DeletePatch
	err := json.Unmarshal(
		[]byte(`["", "a/b/c/d","d/e","f","/g","g/h/i"]`),
		&patch,
	)
	require.NoError(t, err)

	assert.Equal(t, DeletePatch{
		"",
		"a/b/c/d",
		"a/b/c/d/e",
		"a/b/c/d/f",
		"g",
		"g/h/i",
	}, patch)
}

// we shouldn't be able to override a state set by tap
func TestPatchStateOverrideTapState(t *testing.T) {
	pe := &PatchEntry{
		State: PatchEntryInfoAutoSnapshot,
	}
	pe.SetState(PatchEntryStateIn)
	assert.Equal(t, PatchEntryInfoAutoSnapshot, pe.State)
}

// We should be able to override a state that is found to be different later
// Use case is that eval response post processing might mark something as an extra read/write
// but then find it in a symlink, depfile, etc. and change the state.
func TestPatchStateOverrideExtraRead(t *testing.T) {
	pe := &PatchEntry{
		State: PatchEntryStateExtraRead,
	}
	pe.SetState(PatchEntryStateDep)
	assert.Equal(t, PatchEntryStateDep, pe.State)
}

func TestChrootTransform(t *testing.T) {
	assert.Equal(t, "chroot", ChrootTransform("/chroot", ""))
	assert.Equal(t, "", ChrootTransform("/chroot/", "chroot"))
	assert.Equal(t, "", ChrootTransform("/chroot", "chroot"))
	assert.Equal(t, "../chroot_file", ChrootTransform("/chroot_file", "chroot"))
	assert.Equal(t, "../chroot_file", ChrootTransform("chroot_file", "chroot"))
	assert.Equal(t, "a/b/c", ChrootTransform("/chroot/a/b/c", "chroot"))
	assert.Equal(t, "a/b/c", ChrootTransform("chroot/a/b/c", "chroot"))
	assert.Equal(t, "../a/b/c", ChrootTransform("/a/b/c", "chroot"))
	assert.Equal(t, "../a/b/c", ChrootTransform("a/b/c", "chroot"))
}
