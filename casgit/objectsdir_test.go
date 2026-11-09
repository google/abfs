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

package casgit

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

// objectsFS creates git objects directories as casfs does: an existing name fails with EEXIST.
type objectsFS struct {
	bindcas.CASFS
	created []string
}

func (f *objectsFS) CreateGitObjects(path, directory string) error {
	dir := filepath.Join(path, directory)
	if _, err := os.Lstat(dir); err == nil {
		return &os.PathError{Op: "mkdir", Path: dir, Err: syscall.EEXIST}
	}
	f.created = append(f.created, dir)
	return os.Mkdir(dir, 0755)
}

func (f *objectsFS) SetExcluded(path string, val bool) error {
	return nil
}

// noRefsStore has no refs, so a clone stops when it resolves its branch.
type noRefsStore struct {
	entities.CASStore
}

func (s *noRefsStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	return make([]entities.Hash, len(refs)), nil
}

func (s *noRefsStore) SearchRefs(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return nil, nil
}

func TestClone_SharedObjectsDir(t *testing.T) {
	tests := []struct {
		name        string
		existing    func(objects string) error
		wantCreated bool
		wantErr     string
	}{
		{name: "first clone creates it", wantCreated: true},
		{name: "a later clone reuses it", existing: func(objects string) error { return os.Mkdir(objects, 0755) }},
		{name: "a file in its place", existing: func(objects string) error { return os.WriteFile(objects, nil, 0644) }, wantErr: "is not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			objects := filepath.Join(root, "project-objects", "platform", "shared.git", "objects")
			require.NoError(t, os.MkdirAll(filepath.Dir(objects), 0755))
			if tt.existing != nil {
				require.NoError(t, tt.existing(objects))
			}
			casfs := &objectsFS{}
			err := NewWithCASFS(&noRefsStore{}, casfs).Clone(context.Background(), CloneOptions{
				RemoteRepo: "https://gerrit.example.com/platform/shared",
				Directory:  filepath.Join(root, "b", "shared"),
				GitDir:     filepath.Join(root, "projects", "b", "shared.git"),
				ObjectsDir: objects,
				Branch:     "release",
				Quiet:      true,
			})
			// The store has no refs: every clone fails after the objects directory, at the branch.
			require.Error(t, err)
			require.NotErrorIs(t, err, fs.ErrExist)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.ErrorContains(t, err, "release")
			if tt.wantCreated {
				require.Equal(t, []string{objects}, casfs.created)
			} else {
				require.Empty(t, casfs.created)
			}
		})
	}
}
