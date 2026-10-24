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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

type refsStore struct {
	entities.CASStore
	refs map[string]entities.Hash
}

func (s *refsStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	hashes := make([]entities.Hash, len(refs))
	for i, ref := range refs {
		hashes[i] = s.refs[ref]
	}
	return hashes, nil
}

func newTestClone(t *testing.T, store entities.CASStore, opts CloneOptions) *GitClone {
	opts.Origin = "origin"
	opts.SingleBranch = true
	opts.DetachedHead = true
	opts.GitDir = t.TempDir()
	return &GitClone{
		CloneOptions: opts,
		GitRepo:      NewGitRepo(NewWithCASFS(store, nil), "", opts.GitDir, ""),
	}
}

func TestClone_ResolvedRefMatchesLookup(t *testing.T) {
	branchHash := entities.NewHash("1111111111111111111111111111111111111111")
	tagHash := entities.NewHash("2222222222222222222222222222222222222222")
	store := &refsStore{refs: map[string]entities.Hash{
		"refs/heads/main": branchHash,
		"refs/tags/v1":    tagHash,
	}}

	tests := []struct {
		name      string
		branch    string
		ref       string
		hash      entities.Hash
		wantFetch string
	}{
		{name: "branch", branch: "main", ref: "refs/heads/main", hash: branchHash, wantFetch: "refs/heads/main:refs/remotes/origin/main"},
		{name: "tag", branch: "v1", ref: "refs/tags/v1", hash: tagHash, wantFetch: "refs/tags/v1:refs/tags/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx := context.Background()

			lookup := newTestClone(t, store, CloneOptions{Branch: tt.branch})
			require.NoError(lookup.selectSource(ctx))

			resolved := newTestClone(t, nil, CloneOptions{Commit: tt.hash, Ref: tt.ref, NoTags: true})
			require.NoError(resolved.selectSource(ctx))

			require.Equal(lookup.cloneSource, resolved.cloneSource)
			require.Equal(lookup.branchRef, resolved.branchRef)
			require.Equal(lookup.Branch, resolved.Branch)
			require.Equal(lookup.sourceHash, resolved.sourceHash)

			resolved.commitHash = tt.hash
			require.NoError(resolved.writeConfig())
			cfg, err := ReadGitConfig(resolved.GitDir)
			require.NoError(err)
			fetch, _, err := cfg.Get("remote.origin.fetch")
			require.NoError(err)
			require.Equal(tt.wantFetch, fetch)

			refs, err := resolved.getRefs()
			require.NoError(err)
			require.Equal(map[string]string{"refs/remotes/origin/" + tt.branch: tt.hash.String()}, refs)
		})
	}
}

func TestClone_CommitWithoutRef(t *testing.T) {
	require := require.New(t)

	h := entities.NewHash("3333333333333333333333333333333333333333")
	c := newTestClone(t, nil, CloneOptions{Commit: h})
	require.NoError(c.selectSource(context.Background()))
	require.Equal(cloneSourceCommit, c.cloneSource)
	require.Equal(h, c.sourceHash)
	require.Empty(c.branchRef)
}
