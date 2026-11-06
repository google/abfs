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

package workspaces

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/pkg/xattr"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/bindcas"
)

// objectsFS creates git objects dirs as casfs does: an existing name fails with EEXIST. With
// createdByOther, the first call finds the dir created by another checkout after the stat.
type objectsFS struct {
	bindcas.CASFS
	createdByOther bool
	// fileByOther makes the other checkout leave a file instead of a dir.
	fileByOther bool
	calls       int
}

func (f *objectsFS) CreateGitObjects(path, directory string) error {
	f.calls++
	dir := filepath.Join(path, directory)
	if f.createdByOther {
		f.createdByOther = false
		if f.fileByOther {
			if err := os.WriteFile(dir, nil, 0644); err != nil {
				return err
			}
		} else if err := os.Mkdir(dir, 0755); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(dir); err == nil {
		return &xattr.Error{Op: "xattr.LSet", Path: path, Name: "casfs.ctrl", Err: syscall.EEXIST}
	}
	return os.Mkdir(dir, 0755)
}

func (f *objectsFS) SetExcluded(path string, val bool) error {
	return nil
}

func TestSetupEmptyGitRepos_SharedObjectsDir(t *testing.T) {
	const objects = ".repo/project-objects/platform/foo.git/objects"
	tests := []struct {
		name           string
		workDirs       []string
		existing       func(objects string) error
		createdByOther bool
		fileByOther    bool
		wantCalls      int
		wantErr        bool
	}{
		{name: "a second checkout reuses it", workDirs: []string{"external/foo", "vendor/foo"}, wantCalls: 1},
		{name: "created by another checkout after the stat", workDirs: []string{"external/foo"}, createdByOther: true, wantCalls: 1},
		{
			name:     "a file in its place",
			workDirs: []string{"external/foo"},
			existing: func(objects string) error { return os.WriteFile(objects, nil, 0644) },
			wantErr:  true,
		},
		{
			name:           "a file left by another checkout after the stat",
			workDirs:       []string{"external/foo"},
			createdByOther: true,
			fileByOther:    true,
			wantErr:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			// SetupEmptyGitRepos creates the .git symlink of the work dir relative to the cwd.
			t.Chdir(root)
			// The root sync restores the parent of the objects dir.
			require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(objects)), 0755))
			if tt.existing != nil {
				require.NoError(t, tt.existing(filepath.Join(root, objects)))
			}
			casfs := &objectsFS{createdByOther: tt.createdByOther, fileByOther: tt.fileByOther}
			for _, wd := range tt.workDirs {
				gitDir := ".repo/projects/" + wd + ".git"
				err := SetupEmptyGitRepos(casfs, root, wd, gitDir, objects)
				if tt.wantErr {
					require.ErrorContains(t, err, filepath.Join(root, objects)+" is not a directory")
					return
				}
				require.NoError(t, err)
				info, err := os.Stat(filepath.Join(root, gitDir, "objects", "pack"))
				require.NoError(t, err)
				require.True(t, info.IsDir())
			}
			require.Equal(t, tt.wantCalls, casfs.calls)
		})
	}
}
