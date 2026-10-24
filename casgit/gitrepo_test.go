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
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

type refKey struct {
	server  string
	project string
}

type refKeyStore struct {
	entities.CASStore
	keys []refKey
}

func (s *refKeyStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	s.keys = append(s.keys, refKey{server, project})
	return nil, os.ErrNotExist
}

type refKeyCASFS struct {
	bindcas.CASFS
}

func (s *refKeyCASFS) SetExcluded(path string, val bool) error {
	return nil
}

func (s *refKeyCASFS) CreateGitObjects(path, directory string) error {
	return nil
}

func TestCloneRefKey(t *testing.T) {
	tests := []struct {
		name       string
		remoteRepo string
		refServer  string
		refProject string
		want       refKey
	}{
		{
			name:       "from the URL",
			remoteRepo: "https://gerrit.example.com/a/platform/foo",
			want:       refKey{"gerrit.example.com", "a/platform/foo"},
		},
		{
			name:       "from the caller",
			remoteRepo: "https://gerrit.example.com/a/platform/foo",
			refServer:  "gerrit.example.com",
			refProject: "platform/foo",
			want:       refKey{"gerrit.example.com", "platform/foo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			store := &refKeyStore{}
			g := NewWithCASFS(store, &refKeyCASFS{})
			err := g.Clone(context.Background(), CloneOptions{
				RemoteRepo: tt.remoteRepo,
				RefServer:  tt.refServer,
				RefProject: tt.refProject,
				Directory:  path.Join(t.TempDir(), "foo"),
				Branch:     "main",
				Quiet:      true,
			})

			require.ErrorContains(err, "remote branch main not found")
			require.Equal([]refKey{tt.want}, store.keys)
		})
	}
}

func TestFetchRefKey(t *testing.T) {
	tests := []struct {
		name       string
		refServer  string
		refProject string
		want       refKey
	}{
		{
			name: "from the remote URL",
			want: refKey{"gerrit.example.com", "a/platform/foo"},
		},
		{
			name:       "from the caller",
			refServer:  "gerrit.example.com",
			refProject: "platform/foo",
			want:       refKey{"gerrit.example.com", "platform/foo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			gitDir := t.TempDir()
			require.NoError(os.MkdirAll(path.Join(gitDir, "refs", "heads"), 0755))
			require.NoError(os.WriteFile(path.Join(gitDir, "HEAD"), []byte("4b825dc642cb6eb9a060e54bf8d69288fbee4904\n"), 0644))
			config := "[remote \"origin\"]\n\turl = https://gerrit.example.com/a/platform/foo\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
			require.NoError(os.WriteFile(path.Join(gitDir, "config"), []byte(config), 0644))

			gitRepo := NewGitRepo(NewWithCASFS(&refKeyStore{}, &refKeyCASFS{}), gitDir, gitDir, path.Join(gitDir, "objects"))
			require.NoError(gitRepo.Load())
			f := &GitFetch{
				FetchOptions: FetchOptions{
					Remote:     "origin",
					GitDir:     gitDir,
					RefServer:  tt.refServer,
					RefProject: tt.refProject,
				},
				GitRepo: gitRepo,
			}

			require.NoError(f.setRemoteInfo())
			require.Equal(tt.want, refKey{f.Server(), f.Project()})
		})
	}
}
