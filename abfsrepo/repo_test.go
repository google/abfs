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
	"testing"
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
