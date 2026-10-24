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

package abfsrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/manifest"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

// revisionStore answers ref lookups as the stores do and records the objects a clone asks for; it holds none.
type revisionStore struct {
	entities.CASStore
	refs      map[[2]string]map[string]entities.Hash
	searchErr error

	mu      sync.Mutex
	objects []entities.Hash
	gets    int
}

func (s *revisionStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	s.mu.Lock()
	s.gets++
	s.mu.Unlock()
	hashes := make([]entities.Hash, len(refs))
	for i, ref := range refs {
		hashes[i] = s.refs[[2]string{server, project}][ref]
	}
	return hashes, nil
}

func (s *revisionStore) SearchRefs(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	var results []entities.ProjectRefHash
	for key, refs := range s.refs {
		if key[0] != server || (project != "" && project != key[1]) {
			continue
		}
		if h, ok := refs[refPrefix]; ok {
			results = append(results, entities.ProjectRefHash{Project: key[1], Ref: refPrefix, Hash: h})
			continue
		}
		for ref, h := range refs {
			if strings.HasPrefix(ref, refPrefix+"/") {
				results = append(results, entities.ProjectRefHash{Project: key[1], Ref: ref, Hash: h})
			}
		}
	}
	return results, nil
}

func (s *revisionStore) GetObject(ctx context.Context, key entities.Hash) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects = append(s.objects, key)
	return nil, os.ErrNotExist
}

func (s *revisionStore) requested() []entities.Hash {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]entities.Hash(nil), s.objects...)
}

func (s *revisionStore) refLookups() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

type revisionCASFS struct {
	bindcas.CASFS
}

func (s *revisionCASFS) SetExcluded(path string, val bool) error {
	return nil
}

func (s *revisionCASFS) CreateGitObjects(path, directory string) error {
	return nil
}

func newRevisionRepo(t *testing.T, store *revisionStore, manifestXML string) *Repo {
	t.Helper()
	require := require.New(t)

	m, err := manifest.Parse([]byte(manifestXML))
	require.NoError(err)

	casfs := &revisionCASFS{}
	r := NewRepoForceRoot(RepoOpts{
		Store: store,
		CASFS: casfs,
		Git:   casgit.NewWithCASFS(store, casfs),
	}, t.TempDir())
	r.flatMan = m
	r.manifestURL = "https://gerrit.example.com/platform/manifest"
	r.groupsInclude, r.groupsExclude = FilterGroups(nil)
	r.filterProjectsByGroups()
	r.curProjects = make(map[string]struct{})
	r.curCopyFiles = make(map[string]struct{})
	r.curLinkFiles = make(map[string]struct{})
	return r
}

func TestSync_RevisionForms(t *testing.T) {
	branchHash := entities.NewHash("1111111111111111111111111111111111111111")
	tagHash := entities.NewHash("2222222222222222222222222222222222222222")
	bothBranchHash := entities.NewHash("3333333333333333333333333333333333333333")
	bothTagHash := entities.NewHash("4444444444444444444444444444444444444444")
	pinnedHash := entities.NewHash("5555555555555555555555555555555555555555")

	tests := []struct {
		name     string
		revision string
		want     entities.Hash
	}{
		{name: "bare branch", revision: "main", want: branchHash},
		{name: "full branch ref", revision: "refs/heads/main", want: branchHash},
		{name: "bare tag", revision: "v1", want: tagHash},
		{name: "full tag ref", revision: "refs/tags/v1", want: tagHash},
		{name: "branch and tag of the same name", revision: "both", want: bothBranchHash},
		{name: "commit", revision: pinnedHash.String(), want: pinnedHash},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			store := &revisionStore{refs: map[[2]string]map[string]entities.Hash{
				{"gerrit.example.com", "platform/foo"}: {
					"refs/heads/main": branchHash,
					"refs/tags/v1":    tagHash,
					"refs/heads/both": bothBranchHash,
					"refs/tags/both":  bothTagHash,
				},
			}}
			r := newRevisionRepo(t, store, fmt.Sprintf(`
<manifest>
  <remote name="origin" fetch="https://gerrit.example.com/" />
  <default remote="origin" />
  <project name="platform/foo" path="foo" revision=%q />
</manifest>
`, tt.revision))

			// The store holds no commit objects, so the sync fails after the first
			// clone has picked its commit.
			require.Error(r.sync(context.Background(), nil, SyncOpts{Jobs: 1}))
			require.NotEmpty(store.requested(), "the clone did not reach a commit")
			require.Equal(tt.want, store.requested()[0])
		})
	}
}

func TestSync_RevisionLookupFallsBackToClone(t *testing.T) {
	branchHash := entities.NewHash("1111111111111111111111111111111111111111")
	const manifestXML = `
<manifest>
  <remote name="origin" fetch="https://gerrit.example.com/" />
  <default revision="main" remote="origin" />
  <project name="platform/foo" path="foo" />
</manifest>
`
	tests := []struct {
		name       string
		refs       map[string]entities.Hash
		searchErr  error
		wantCommit bool
	}{
		// A search that fails, as an unknown host does through cacheman, leaves every
		// project to the clone's own lookup.
		{name: "search fails", refs: map[string]entities.Hash{"refs/heads/main": branchHash}, searchErr: errors.New("unavailable"), wantCommit: true},
		{name: "project not on the server", refs: nil, wantCommit: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			store := &revisionStore{
				refs:      map[[2]string]map[string]entities.Hash{{"gerrit.example.com", "platform/foo"}: tt.refs},
				searchErr: tt.searchErr,
			}
			r := newRevisionRepo(t, store, manifestXML)
			require.Error(r.sync(context.Background(), nil, SyncOpts{Jobs: 1}))
			require.Empty(r.resolved)
			require.Positive(store.refLookups(), "the clone did not look the branch up")
			if tt.wantCommit {
				require.Equal([]entities.Hash{branchHash}, store.requested())
			} else {
				require.Empty(store.requested())
			}
		})
	}
}
