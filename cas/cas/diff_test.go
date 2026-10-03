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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiff_NilHashes(t *testing.T) {
	require.NoError(t, Diff(nil, nil, nil, true, nil))
}
func TestDiff_TopLevelSame(t *testing.T) {
	ts := newTestStore()
	fileName := "file"
	f := DirEntry{Hash: hashFile(fileName, 300), Size: 300, Mode: FileModeFile(), Name: fileName}
	h, err := ts.SaveDirEntries(NewDirInMemory([]DirEntry{f}))
	require.NoError(t, err)
	require.NoError(t, Diff(h, h, ts, true, nil))
}

type ai struct {
	action DiffAction
	mode   FileMode
	hash   string
	name   string
}

func TestDiff_TopLevelDiff(t *testing.T) {
	ts := newTestStore()
	fileName1 := "file1"
	fileName2 := "file2"
	fHash1 := hashFile(fileName1, 300)
	fHash2 := hashFile(fileName2, 301)
	f1 := DirEntry{Hash: fHash1, Size: 300, Mode: FileModeFile(), Name: fileName1}
	f2 := DirEntry{Hash: fHash2, Size: 301, Mode: FileModeFile(), Name: fileName2}

	h1, err := ts.SaveDirEntries(NewDirInMemory([]DirEntry{f1}))
	h2, err := ts.SaveDirEntries(NewDirInMemory([]DirEntry{f2}))

	require.NoError(t, err)
	var diffs []ai
	dh := func(action DiffAction, dirPath string, de *DirEntry) {
		diffs = append(diffs, ai{action: action, mode: de.Mode, hash: de.Hash.String(), name: filepath.Join(dirPath, de.Name)})
	}

	require.NoError(t, Diff(h1, h2, ts, true, dh))
	var expDiffs []ai = []ai{
		ai{action: DiffActionRemove, mode: FileModeDir(), hash: h1.String(), name: "."},
		ai{action: DiffActionAdd, mode: FileModeDir(), hash: h2.String(), name: "."},
		ai{action: DiffActionRemove, mode: FileModeFile(), hash: fHash1.String(), name: fileName1},
		ai{action: DiffActionAdd, mode: FileModeFile(), hash: fHash2.String(), name: fileName2},
	}
	require.Equal(t, expDiffs, diffs)
}
