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
	"github.com/google/abfs/shared/entities"
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

func (f *objectsFS) NodeType(path string) (string, error) {
	return "dir", nil
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

// submoduleFS keeps nodes on the local disk and answers for them as casfs does: the paths in
// commits are submodule entries, which take entries but no deeptree.
type submoduleFS struct {
	objectsFS
	commits   map[string]bool
	excluded  map[string]bool
	deepTrees []string
}

func (f *submoduleFS) NodeType(path string) (string, error) {
	if _, err := os.Lstat(path); err != nil {
		return "", err
	}
	if f.commits[path] {
		return bindcas.NodeTypeCommit, nil
	}
	return "dir", nil
}

func (f *submoduleFS) Excluded(path string) (bool, error) {
	return f.excluded[path], nil
}

func (f *submoduleFS) SetExcluded(path string, val bool) error {
	f.excluded[path] = val
	return nil
}

func (f *submoduleFS) Remove(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	delete(f.commits, path)
	return nil
}

func (f *submoduleFS) Mkdir(name string, m entities.FileMode) error {
	return os.Mkdir(name, os.FileMode(m))
}

func (f *submoduleFS) SetDeepTree(path string, h entities.Hash) error {
	if f.commits[path] {
		return &xattr.Error{Op: "xattr.LSet", Path: path, Name: "casfs.deeptree", Err: syscall.EOPNOTSUPP}
	}
	f.deepTrees = append(f.deepTrees, path)
	return nil
}

// A restore of an outer work tree saved with a submodule entry at the path of an inner project
// leaves that entry there. Setting up the inner git dirs replaces it before the .git link goes
// into it, so the deeptree of the inner work tree can be set afterwards.
func TestSetupEmptyGitRepos_SubmoduleEntry(t *testing.T) {
	const workDir = "sub/inner"
	const gitDir = ".repo/projects/sub/inner.git"
	const objects = ".repo/project-objects/test/inner.git/objects"
	root := t.TempDir()
	t.Chdir(root)
	workDirAbs := filepath.Join(root, workDir)
	require.NoError(t, os.MkdirAll(workDirAbs, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(objects)), 0755))
	casfs := &submoduleFS{commits: map[string]bool{workDirAbs: true}, excluded: map[string]bool{}}

	require.NoError(t, SetupEmptyGitRepos(casfs, root, workDir, gitDir, objects))
	require.NoError(t, bindcas.SetWorkTreeDeepTree(casfs, workDirAbs, entities.Hash{}))
	require.Equal(t, []string{workDirAbs}, casfs.deepTrees)
	require.True(t, casfs.excluded[workDirAbs])
	target, err := os.Readlink(filepath.Join(workDirAbs, ".git"))
	require.NoError(t, err)
	require.Equal(t, "../../.repo/projects/sub/inner.git", target)
}
