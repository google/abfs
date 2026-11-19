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

package abfsrepo_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/abfsrepo"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/entities"
)

type refKey struct {
	server  string
	project string
}

// refKeyStore records the server and project of every ref lookup and finds nothing.
type refKeyStore struct {
	stubCASStore
	mu   sync.Mutex
	keys []refKey
}

func (s *refKeyStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, refKey{server, project})
	return nil, os.ErrNotExist
}

func (s *refKeyStore) recorded() []refKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]refKey(nil), s.keys...)
}

func TestSync_RefKey(t *testing.T) {
	tests := []struct {
		name     string
		fetch    string
		project  string
		upstream string
		want     []refKey
	}{
		{
			name:    "gerrit /a/ prefix",
			fetch:   "https://gerrit.example.com/a/",
			project: "platform/foo",
			want:    []refKey{{"gerrit.example.com", "platform/foo"}},
		},
		{
			name:    "path-style remote",
			fetch:   "https://github.com/google/",
			project: "uuid",
			want:    []refKey{{"github.com", "uuid"}},
		},
		{
			name:    "host remote",
			fetch:   "https://android.googlesource.com/",
			project: "platform/build",
			want:    []refKey{{"android.googlesource.com", "platform/build"}},
		},
		{
			// The upstream branch differs from the revision, so the optimized git
			// fallback clones it first.
			name:     "optimized fallback",
			fetch:    "https://gerrit.example.com/a/",
			project:  "platform/foo",
			upstream: "dev",
			want:     []refKey{{"gerrit.example.com", "platform/foo"}, {"gerrit.example.com", "platform/foo"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx := context.Background()

			testDir := t.TempDir()
			manifestRemote := filepath.Join(testDir, "manifest.git")
			gitRepoRemote := filepath.Join(testDir, "git-repo.git")
			manifestXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<manifest>
  <remote name="origin" fetch=%q />
  <default revision="main" remote="origin" />
  <project path="p" name=%q upstream=%q />
</manifest>
`, tt.fetch, tt.project, tt.upstream)
			setupRemoteRepo(t, manifestRemote, "main", map[string]string{"default.xml": manifestXML})
			setupRemoteRepo(t, gitRepoRemote, "stable", map[string]string{"repo": "#!/bin/sh\n"})

			wsDir := filepath.Join(testDir, "workspace")
			require.NoError(os.MkdirAll(wsDir, 0755))

			// The manifest and the repo tool come from local paths through the git
			// fallback, so only the project clone looks refs up. Its own git fallback
			// must not reach the network.
			t.Setenv("GIT_ALLOW_PROTOCOL", "file")
			store := &refKeyStore{}
			casfs := &stubCASFS{}
			repo := abfsrepo.NewRepoForceRoot(abfsrepo.RepoOpts{
				Store: store,
				CASFS: casfs,
				Git:   casgit.NewWithCASFS(store, casfs),
			}, wsDir)
			require.NoError(repo.Init(ctx, abfsrepo.InitOpts{
				ManifestURL:    manifestRemote,
				ManifestBranch: "main",
				ManifestName:   "default.xml",
				RepoURL:        gitRepoRemote,
				RepoRev:        "stable",
				FallbackToGit:  true,
			}))

			require.Error(repo.Sync(ctx, nil, abfsrepo.SyncOpts{Jobs: 1, FallbackToGit: true}))
			require.Equal(tt.want, store.recorded())
		})
	}
}
