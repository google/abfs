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

package bindcas

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/pkg/xattr"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

// typesFS keeps nodes on the local disk and answers for them as casfs does: the paths in commits
// are submodule entries, and a deeptree is set only on a directory.
type typesFS struct {
	CASFS
	commits     map[string]bool
	excluded    map[string]bool
	deepTreeErr error
	typeErr     error
	deepTrees   []string
	removed     []string
}

func (f *typesFS) NodeType(path string) (string, error) {
	if f.typeErr != nil {
		return "", f.typeErr
	}
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return "", err
	case f.commits[path]:
		return NodeTypeCommit, nil
	case info.Mode()&os.ModeSymlink != 0:
		return "symlink", nil
	case info.IsDir():
		return "dir", nil
	default:
		return "reg", nil
	}
}

func (f *typesFS) SetDeepTree(path string, h entities.Hash) error {
	if f.deepTreeErr != nil {
		return f.deepTreeErr
	}
	if t, err := f.NodeType(path); err != nil || t != "dir" {
		return &xattr.Error{Op: "xattr.LSet", Path: path, Name: "casfs.deeptree", Err: syscall.EOPNOTSUPP}
	}
	f.deepTrees = append(f.deepTrees, path)
	return nil
}

func (f *typesFS) Excluded(path string) (bool, error) {
	return f.excluded[path], nil
}

func (f *typesFS) SetExcluded(path string, val bool) error {
	f.excluded[path] = val
	return nil
}

func (f *typesFS) Remove(path string) error {
	f.removed = append(f.removed, path)
	if err := os.Remove(path); err != nil {
		return err
	}
	delete(f.commits, path)
	delete(f.excluded, path)
	return nil
}

func (f *typesFS) Mkdir(name string, m entities.FileMode) error {
	return os.Mkdir(name, os.FileMode(m))
}

// A submodule entry at the work tree is replaced by a plain directory with the same exclusion
// before the deeptree is set. Nothing else is removed: a file or a symlink keeps the error of the
// deeptree set.
func TestSetWorkTreeDeepTree(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(path string) error
		commit      bool
		excluded    bool
		deepTreeErr error
		typeErr     error
		wantErr     []error
		wantRemoved bool
	}{
		{name: "directory", setup: mkdir},
		{name: "submodule entry", setup: mkdir, commit: true, wantRemoved: true},
		{name: "excluded submodule entry", setup: mkdir, commit: true, excluded: true, wantRemoved: true},
		{
			name: "submodule entry with entries",
			setup: func(path string) error {
				if err := os.Mkdir(path, 0755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(path, "work.txt"), nil, 0644)
			},
			commit:      true,
			wantErr:     []error{syscall.ENOTEMPTY},
			wantRemoved: true,
		},
		{name: "file", setup: func(path string) error { return os.WriteFile(path, nil, 0644) }, wantErr: []error{syscall.EOPNOTSUPP}},
		{name: "symlink", setup: func(path string) error { return os.Symlink(".", path) }, wantErr: []error{syscall.EOPNOTSUPP}},
		{name: "other error", setup: mkdir, commit: true, deepTreeErr: syscall.EBUSY, wantErr: []error{syscall.EBUSY}},
		{name: "type not readable", setup: mkdir, commit: true, typeErr: syscall.ENODATA, wantErr: []error{syscall.ENODATA}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "inner")
			require.NoError(t, tt.setup(dir))
			before, err := os.Lstat(dir)
			require.NoError(t, err)
			casfs := &typesFS{
				commits:     map[string]bool{dir: tt.commit},
				excluded:    map[string]bool{dir: tt.excluded},
				deepTreeErr: tt.deepTreeErr,
				typeErr:     tt.typeErr,
			}

			err = SetWorkTreeDeepTree(casfs, dir, entities.Hash{})
			if tt.wantRemoved {
				require.Equal(t, []string{dir}, casfs.removed)
			} else {
				require.Empty(t, casfs.removed)
			}
			if len(tt.wantErr) > 0 {
				for _, want := range tt.wantErr {
					require.ErrorIs(t, err, want)
				}
				require.Empty(t, casfs.deepTrees)
				after, err := os.Lstat(dir)
				require.NoError(t, err)
				require.Equal(t, before.Mode(), after.Mode())
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{dir}, casfs.deepTrees)
			require.DirExists(t, dir)
			require.Equal(t, tt.excluded, casfs.excluded[dir])
		})
	}
}

func mkdir(path string) error {
	return os.Mkdir(path, 0755)
}

// ReplaceSubmoduleEntry leaves every node that is not a submodule entry alone.
func TestReplaceSubmoduleEntry(t *testing.T) {
	root := t.TempDir()
	dir, file, link := filepath.Join(root, "dir"), filepath.Join(root, "file"), filepath.Join(root, "link")
	require.NoError(t, os.Mkdir(dir, 0755))
	require.NoError(t, os.WriteFile(file, nil, 0644))
	require.NoError(t, os.Symlink("dir", link))
	casfs := &typesFS{commits: map[string]bool{}, excluded: map[string]bool{}}
	for _, p := range []string{dir, file, link} {
		replaced, err := ReplaceSubmoduleEntry(casfs, p)
		require.NoError(t, err)
		require.False(t, replaced)
	}
	require.Empty(t, casfs.removed)

	_, err := ReplaceSubmoduleEntry(casfs, filepath.Join(root, "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
