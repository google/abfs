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
	"testing"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/entities"
)

func TestRelativeToRoot(t *testing.T) {
	tests := []struct {
		name        string
		repoRoot    string
		cwd         string
		inputPath   string
		expected    string
		expectError bool
	}{
		{
			name:        "error when cwd is empty",
			repoRoot:    "/repo/root",
			cwd:         "",
			inputPath:   ".",
			expected:    "",
			expectError: true,
		},
		{
			name:        "current directory (.)",
			repoRoot:    "/repo/root",
			cwd:         "/repo/root/a/b",
			inputPath:   ".",
			expected:    "a/b",
			expectError: false,
		},
		{
			name:        "child directory",
			repoRoot:    "/repo/root",
			cwd:         "/repo/root/a/b",
			inputPath:   "c",
			expected:    "a/b/c",
			expectError: false,
		},
		{
			name:        "parent directory via relative path",
			repoRoot:    "/repo/root",
			cwd:         "/repo/root/a/b",
			inputPath:   "../c",
			expected:    "a/c",
			expectError: false,
		},
		{
			name:        "absolute path inside repo",
			repoRoot:    "/repo/root",
			cwd:         "/repo/root/a/b",
			inputPath:   "/repo/root/x/y",
			expected:    "x/y",
			expectError: false,
		},
		{
			name:        "relative path escaping repo root",
			repoRoot:    "/repo/root",
			cwd:         "/repo/root/a",
			inputPath:   "../../outside",
			expected:    "",
			expectError: true,
		},
		{
			name:        "absolute path outside repo root",
			repoRoot:    "/repo/root",
			cwd:         "/repo/root/a",
			inputPath:   "/some/outside/path",
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Repo{
				repoRoot: tt.repoRoot,
				cwd:      tt.cwd,
			}

			got, err := r.RelativeToRoot(tt.inputPath)
			if (err != nil) != tt.expectError {
				t.Fatalf("expected error: %v, got: %v", tt.expectError, err)
			}

			if got != tt.expected {
				t.Errorf("expected path %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestSyncOptsFallbackToGit(t *testing.T) {
	opts := SyncOpts{
		Jobs:          4,
		FallbackToGit: true,
	}
	if !opts.FallbackToGit {
		t.Errorf("expected FallbackToGit to be true")
	}
}

func TestInitOptsFallbackToGit(t *testing.T) {
	opts := InitOpts{
		ManifestURL:   "https://example.com/manifest",
		FallbackToGit: true,
	}
	if !opts.FallbackToGit {
		t.Errorf("expected FallbackToGit to be true")
	}
}

func TestDetermineFallbackBranch(t *testing.T) {
	tests := []struct {
		name            string
		upstream        string
		defaultRevision string
		revision        string
		expectedBranch  string
		expectedOk      bool
	}{
		{
			name:            "revision is a hash with defaultRevision",
			upstream:        "",
			defaultRevision: "main",
			revision:        "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			expectedBranch:  "main",
			expectedOk:      true,
		},
		{
			name:            "upstream overrides defaultRevision",
			upstream:        "dev",
			defaultRevision: "main",
			revision:        "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			expectedBranch:  "dev",
			expectedOk:      true,
		},
		{
			name:            "upstream is a hash so falls back to defaultRevision",
			upstream:        "cafebabecafebabecafebabecafebabecafebabe",
			defaultRevision: "main",
			revision:        "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			expectedBranch:  "main",
			expectedOk:      true,
		},
		{
			name:            "defaultRevision is HEAD",
			upstream:        "",
			defaultRevision: "HEAD",
			revision:        "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			expectedBranch:  "",
			expectedOk:      true,
		},
		{
			name:            "no upstream and no defaultRevision with hash revision",
			upstream:        "",
			defaultRevision: "",
			revision:        "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			expectedBranch:  "",
			expectedOk:      true,
		},
		{
			name:            "revision matches default branch",
			upstream:        "",
			defaultRevision: "main",
			revision:        "main",
			expectedBranch:  "",
			expectedOk:      false,
		},
		{
			name:            "revision is a different branch from default branch",
			upstream:        "",
			defaultRevision: "main",
			revision:        "feature",
			expectedBranch:  "main",
			expectedOk:      true,
		},
		{
			name:            "revision matches upstream",
			upstream:        "dev",
			defaultRevision: "main",
			revision:        "dev",
			expectedBranch:  "",
			expectedOk:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			branch, ok := determineFallbackBranch(tt.upstream, tt.defaultRevision, tt.revision)
			if ok != tt.expectedOk {
				t.Fatalf("determineFallbackBranch() ok = %v, expected %v", ok, tt.expectedOk)
			}
			if branch != tt.expectedBranch {
				t.Fatalf("determineFallbackBranch() branch = %q, expected %q", branch, tt.expectedBranch)
			}
		})
	}
}

// manifestRefsStore answers ref lookups from a fixed map and records the keys it was asked for.
type manifestRefsStore struct {
	entities.CASStore
	refs    map[string]entities.Hash
	err     error
	lookups [][2]string
}

func (s *manifestRefsStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	s.lookups = append(s.lookups, [2]string{server, project})
	if s.err != nil {
		return nil, s.err
	}
	hashes := make([]entities.Hash, len(refs))
	for i, ref := range refs {
		hashes[i] = s.refs[ref]
	}
	return hashes, nil
}

func TestManifestTarget(t *testing.T) {
	branchHash := entities.NewHash("1111111111111111111111111111111111111111")
	tagHash := entities.NewHash("2222222222222222222222222222222222222222")

	tests := []struct {
		name          string
		branch        string
		refs          map[string]entities.Hash
		err           error
		wantTarget    string
		wantGitTarget string
		wantLookup    bool
	}{
		{
			name:          "branch",
			branch:        "main",
			refs:          map[string]entities.Hash{"refs/heads/main": branchHash},
			wantTarget:    "main",
			wantGitTarget: "refs/heads/main",
			wantLookup:    true,
		},
		{
			name:          "tag",
			branch:        "v1",
			refs:          map[string]entities.Hash{"refs/tags/v1": tagHash},
			wantTarget:    tagHash.String(),
			wantGitTarget: tagHash.String(),
			wantLookup:    true,
		},
		{
			name:          "branch and tag of one name",
			branch:        "v1",
			refs:          map[string]entities.Hash{"refs/heads/v1": branchHash, "refs/tags/v1": tagHash},
			wantTarget:    "v1",
			wantGitTarget: "refs/heads/v1",
			wantLookup:    true,
		},
		{
			name:          "commit hash",
			branch:        tagHash.String(),
			wantTarget:    tagHash.String(),
			wantGitTarget: tagHash.String(),
		},
		{
			name:          "not found",
			branch:        "nope",
			wantTarget:    "nope",
			wantGitTarget: "refs/heads/nope",
			wantLookup:    true,
		},
		{
			name:          "lookup error",
			branch:        "v1",
			err:           errors.New("unavailable"),
			wantTarget:    "v1",
			wantGitTarget: "refs/heads/v1",
			wantLookup:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &manifestRefsStore{refs: tt.refs, err: tt.err}
			r := &Repo{
				RepoOpts:       RepoOpts{Git: casgit.New(store)},
				manifestURL:    "https://gerrit.example.com/platform/manifest",
				manifestBranch: "refs/heads/" + tt.branch,
			}

			target, gitTarget := r.manifestTarget(context.Background(), tt.branch)
			if target != tt.wantTarget {
				t.Errorf("expected target %q, got %q", tt.wantTarget, target)
			}
			if gitTarget != tt.wantGitTarget {
				t.Errorf("expected git target %q, got %q", tt.wantGitTarget, gitTarget)
			}

			var wantLookups [][2]string
			if tt.wantLookup {
				wantLookups = [][2]string{{"gerrit.example.com", "platform/manifest"}}
			}
			if len(store.lookups) != len(wantLookups) || (len(wantLookups) > 0 && store.lookups[0] != wantLookups[0]) {
				t.Errorf("expected lookups %v, got %v", wantLookups, store.lookups)
			}
		})
	}
}
