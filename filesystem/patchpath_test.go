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

package filesystem

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

// pathRecorder records the paths the mount passes to the CAS filesystem. Every file is missing.
type pathRecorder struct {
	bindcas.CASFS
	mu    sync.Mutex
	paths []string
}

func (r *pathRecorder) record(p string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, p)
}

func (r *pathRecorder) Lstat(p string) (cas.FileInfo, error) {
	r.record(p)
	return nil, os.ErrNotExist
}

func (r *pathRecorder) Hash(p string) (entities.Hash, error) {
	r.record(p)
	return entities.Hash{}, os.ErrNotExist
}

func (r *pathRecorder) HashStat(p string) (*entities.HashStat, error) {
	r.record(p)
	return nil, os.ErrNotExist
}

func (r *pathRecorder) Remove(p string) error {
	r.record(p)
	return os.ErrNotExist
}

func (r *pathRecorder) MkdirAll(p string, m entities.FileMode) error {
	r.record(p)
	return nil
}

func newRecordingMount() (*CasfsMount, *pathRecorder) {
	r := &pathRecorder{}
	m := NewCasfsMount(nil)
	m.casfs = r
	return m, r
}

var fileHash = entities.NewHash("1111111111111111111111111111111111111111")

func TestMatchAndPatch_PathsOutsideTheMount(t *testing.T) {
	tests := []struct {
		name    string
		chroot  string
		read    entities.Patch
		write   entities.Patch
		deletes entities.DeletePatch
	}{
		{
			name:   "read below a top",
			chroot: "android",
			read:   entities.Patch{{Path: "../../etc/passwd", Mode: entities.FileModeRegular, Hash: fileHash}},
		},
		{
			name:  "read at the root",
			read:  entities.Patch{{Path: "../etc/passwd", Mode: entities.FileModeRegular, Hash: fileHash}},
			write: entities.Patch{{Path: "out/x", Mode: entities.FileModeDir}},
		},
		{
			name:  "absolute read",
			read:  entities.Patch{{Path: "/etc/passwd", Mode: entities.FileModeRegular, Hash: fileHash}},
			write: entities.Patch{{Path: "out/x", Mode: entities.FileModeDir}},
		},
		{
			name:   "write",
			chroot: "android",
			write:  entities.Patch{{Path: "out/x", Mode: entities.FileModeDir}, {Path: "../../tmp/x", Mode: entities.FileModeDir}},
		},
		{
			name:    "delete",
			chroot:  "android",
			deletes: entities.DeletePatch{"out/x", "../../tmp/x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, r := newRecordingMount()
			err := m.MatchAndPatch(context.Background(), 1, tt.chroot, tt.read, tt.write, tt.deletes)
			require.ErrorContains(t, err, "outside the mount")
			require.Empty(t, r.paths, "nothing is read or changed")
		})
	}
}

func TestMatchAndPatch_InsideTheMount(t *testing.T) {
	m, r := newRecordingMount()
	write := entities.Patch{{Path: "out/x", Mode: entities.FileModeDir}, {Path: "../tools/out", Mode: entities.FileModeDir}}
	require.NoError(t, m.MatchAndPatch(context.Background(), 1, "android", nil, write, entities.DeletePatch{"out/y"}))
	require.Equal(t, []string{"android/out/y", "android/out/x", "tools/out"}, r.paths)
}

func TestMatches_GitStateAboveATop(t *testing.T) {
	m, r := newRecordingMount()
	read := entities.Patch{
		{Path: "../.repo/projects/other/app.git/HEAD", Mode: entities.FileModeRegular, Hash: fileHash},
		{Path: "../.repo/project-objects/other/app.git/config", Mode: entities.FileModeRegular, Hash: fileHash},
	}
	require.NoError(t, m.Matches(context.Background(), 1, "android", read))
	require.Empty(t, r.paths, "git state is not matched")
}

func TestHashes_PathsOutsideTheMount(t *testing.T) {
	m, r := newRecordingMount()
	result, errs := m.Hashes(context.Background(), 1, "android", []string{"../../etc/passwd", "frameworks/x"})
	require.Equal(t, []string{"", entities.EmptyFileHash.String()}, result)
	require.Contains(t, errs["../etc/passwd"], "outside the mount")
	require.Equal(t, []string{"android/frameworks/x"}, r.paths)

	m, r = newRecordingMount()
	stats, errs := m.HashStats(context.Background(), 1, ".", []string{"../etc/passwd", "frameworks/x"})
	require.Len(t, stats, 2)
	require.Contains(t, errs["../etc/passwd"], "outside the mount")
	require.Equal(t, []string{"frameworks/x"}, r.paths)
}
