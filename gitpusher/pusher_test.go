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

package gitpusher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/shared/entities"
)

func TestRefreshRefObjects_RetryOnBadObject(t *testing.T) {
	tempDir := t.TempDir()

	// Create a fake git executable
	fakeGitPath := filepath.Join(tempDir, "fake_git")
	fakeGitScript := `#!/bin/bash
# Log calls
echo "$@" >> ` + filepath.Join(tempDir, "git_calls.log") + `

if [[ "$1" == "show-ref" ]]; then
	echo "1234567890abcdef1234567890abcdef12345678 refs/heads/main"
	exit 0
fi

if [[ "$*" == *"rev-list"* ]]; then
	# Read stdin if --stdin is present
	if [[ "$*" == *"--stdin"* ]]; then
		stdin_content=$(cat)
		echo "STDIN: $stdin_content" >> ` + filepath.Join(tempDir, "git_calls.log") + `
		if [[ "$stdin_content" == *"^"* ]]; then
			echo "fatal: bad object dummy_sha" >&2
			exit 128
		fi
	fi
	echo "abcdef1234567890abcdef1234567890abcdef12"
	exit 0
fi

exit 0
`
	err := os.WriteFile(fakeGitPath, []byte(fakeGitScript), 0755)
	require.NoError(t, err)

	repoPath := filepath.Join(tempDir, "repo.git")
	err = os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)

	pushedRefsFile := filepath.Join(repoPath, "pushed-refs")
	// Create pushed-refs with a hash that will be used as exclusion
	err = os.WriteFile(pushedRefsFile, []byte("9876543210fedcba9876543210fedcba98765432 refs/heads/main\n"), 0644)
	require.NoError(t, err)

	p := &pusher{
		dirPath:        repoPath,
		pushedRefsFile: pushedRefsFile,
		shgit:          &shellgit.ShellGit{BinPath: fakeGitPath},
		config: &ProjectConfig{
			Server:  "example.com",
			Project: "repo",
		},
	}

	err = p.refreshRefObjects()
	require.NoError(t, err)

	// Verify pushed-refs was deleted
	_, err = os.Stat(pushedRefsFile)
	require.True(t, os.IsNotExist(err), "pushed-refs file should have been deleted")

	// Verify git calls
	calls, err := os.ReadFile(filepath.Join(tempDir, "git_calls.log"))
	require.NoError(t, err)
	callsStr := string(calls)

	// Should have called rev-list twice
	require.Equal(t, 2, strings.Count(callsStr, "rev-list"))
	// First call should have had exclusion in stdin
	require.Contains(t, callsStr, "STDIN: 1234567890abcdef1234567890abcdef12345678\n^9876543210fedcba9876543210fedcba98765432")
}

func TestRefreshRefObjects_RetryOnBadObject_Failure(t *testing.T) {
	tempDir := t.TempDir()

	// Create a fake git executable that always fails on rev-list
	fakeGitPath := filepath.Join(tempDir, "fake_git")
	fakeGitScript := `#!/bin/bash
# Log calls
echo "$@" >> ` + filepath.Join(tempDir, "git_calls.log") + `

if [[ "$1" == "show-ref" ]]; then
	echo "1234567890abcdef1234567890abcdef12345678 refs/heads/main"
	exit 0
fi

if [[ "$*" == *"rev-list"* ]]; then
	echo "fatal: bad object dummy_sha" >&2
	exit 128
fi

exit 0
`
	err := os.WriteFile(fakeGitPath, []byte(fakeGitScript), 0755)
	require.NoError(t, err)

	repoPath := filepath.Join(tempDir, "repo.git")
	err = os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)

	pushedRefsFile := filepath.Join(repoPath, "pushed-refs")
	err = os.WriteFile(pushedRefsFile, []byte("9876543210fedcba9876543210fedcba98765432 refs/heads/main\n"), 0644)
	require.NoError(t, err)

	p := &pusher{
		dirPath:        repoPath,
		pushedRefsFile: pushedRefsFile,
		shgit:          &shellgit.ShellGit{BinPath: fakeGitPath},
		config: &ProjectConfig{
			Server:  "example.com",
			Project: "repo",
		},
	}

	err = p.refreshRefObjects()
	require.Error(t, err)
	require.Contains(t, err.Error(), "error waiting on `git rev-list`")

	// Verify git calls
	calls, err := os.ReadFile(filepath.Join(tempDir, "git_calls.log"))
	require.NoError(t, err)
	callsStr := string(calls)

	// Should have called rev-list twice before giving up
	require.Equal(t, 2, strings.Count(callsStr, "rev-list"))
}

type fakeCASStore struct {
	entities.CASStore
	objects map[entities.Hash][]byte
}

func (f *fakeCASStore) PutObject(ctx context.Context, key entities.Hash, content []byte) error {
	f.objects[key] = content
	return nil
}

func (f *fakeCASStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	return []entities.Hash{entities.ZeroHash}, nil
}

func TestDeepTreeForTree(t *testing.T) {
	t.Run("NormalTree", func(t *testing.T) {
		tempDir := t.TempDir()

		// Create a fake git executable
		fakeGitPath := filepath.Join(tempDir, "fake_git")
		fakeGitScript := `#!/bin/bash
if [[ "$1" == "ls-tree" ]]; then
	# Output some fake ls-tree entries separated by NUL byte
	printf "100644 blob e69de29bb2d1d6434b8b29ae775ad8c2e48c5391 12345\tfoo/bar/baz.txt\0"
	printf "040000 tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904 -\tfoo/bar\0"
	exit 0
fi
exit 0
`
		err := os.WriteFile(fakeGitPath, []byte(fakeGitScript), 0755)
		require.NoError(t, err)

		repoPath := filepath.Join(tempDir, "repo.git")
		err = os.MkdirAll(repoPath, 0755)
		require.NoError(t, err)

		store := &fakeCASStore{objects: make(map[entities.Hash][]byte)}
		p := &pusher{
			dirPath:     repoPath,
			shgit:       &shellgit.ShellGit{BinPath: fakeGitPath},
			store:       store,
			deepTreeMap: make(map[entities.Hash]entities.Hash),
		}

		treeHash := entities.NewHash("abcdef1234567890abcdef1234567890abcdef12")
		entries, err := p.deepTreeForTree(context.Background(), treeHash)
		require.NoError(t, err)

		require.Len(t, entries, 4)
		require.Equal(t, "", entries[0].Name)
		require.Equal(t, entities.FileModeDir, entries[0].Mode)
		require.Equal(t, treeHash, entries[0].Hash)

		require.Equal(t, "foo", entries[1].Name)
		require.Equal(t, entities.FileModeDir, entries[1].Mode)

		require.Equal(t, "bar", entries[2].Name)
		require.Equal(t, entities.FileModeDir, entries[2].Mode)

		require.Equal(t, "baz.txt", entries[3].Name)
		require.Equal(t, entities.FileModeRegular, entries[3].Mode)
		require.Equal(t, int64(12345), entries[3].Size)
	})

	t.Run("ThresholdedTree", func(t *testing.T) {
		tempDir := t.TempDir()

		// Create a fake git executable that generates more than 1000 entries
		fakeGitPath := filepath.Join(tempDir, "fake_git")
		fakeGitScript := `#!/bin/bash
if [[ "$1" == "ls-tree" ]]; then
	# We want foo/bar/file_i.txt to exceed 1000 entries
	i=1
	while [ $i -le 1005 ]; do
		printf "100644 blob e69de29bb2d1d6434b8b29ae775ad8c2e48c5391 12345\tfoo/bar/file_$i.txt\0"
		i=$((i+1))
	done
	printf "040000 tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904 -\tfoo/bar\0"
	exit 0
fi
exit 0
`
		err := os.WriteFile(fakeGitPath, []byte(fakeGitScript), 0755)
		require.NoError(t, err)

		repoPath := filepath.Join(tempDir, "repo.git")
		err = os.MkdirAll(repoPath, 0755)
		require.NoError(t, err)

		store := &fakeCASStore{objects: make(map[entities.Hash][]byte)}
		p := &pusher{
			dirPath:     repoPath,
			shgit:       &shellgit.ShellGit{BinPath: fakeGitPath},
			store:       store,
			deepTreeMap: make(map[entities.Hash]entities.Hash),
		}

		treeHash := entities.NewHash("abcdef1234567890abcdef1234567890abcdef12")
		entries, err := p.deepTreeForTree(context.Background(), treeHash)
		require.NoError(t, err)

		// The root entries should contain foo/bar which is thresholded (FileModeDeepTree)
		// and has the deep tree hash.
		require.Len(t, entries, 3)
		require.Equal(t, "", entries[0].Name)
		require.Equal(t, entities.FileModeDir, entries[0].Mode)

		require.Equal(t, "foo", entries[1].Name)
		require.Equal(t, entities.FileModeDir, entries[1].Mode)

		require.Equal(t, "bar", entries[2].Name)
		require.Equal(t, entities.FileModeDeepTree, entries[2].Mode)

		// The stored objects map should contain the sub-DeepTree object
		require.Len(t, store.objects, 1)

		// Let's assert that the hash matches the entries[2].Hash
		require.Contains(t, store.objects, entries[2].Hash)
	})
}

func TestPusherInit_WithNegativeRefspecs(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "repo.git")

	// Pre-create repo with negative refspec in config
	err := ensureGitRepo(repoPath)
	require.NoError(t, err)

	configContent := `[core]
	repositoryformatversion = 0
	filemode = true
	bare = true
[remote "origin"]
	url = https://example.com/repo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
	fetch = ^refs/changes/*/meta
	fetch = ^refs/changes/*/robot-comments
	fetch = ^refs/cache-automerge/*
`
	err = os.WriteFile(filepath.Join(repoPath, "config"), []byte(configContent), 0644)
	require.NoError(t, err)

	mockRemote := new(MockRemoteProvider)
	mockRemote.On("GetRefs", mock.Anything, "example.com", "repo", []string{binding.PacksRefs}).Return([]entities.Hash{entities.ZeroHash}, nil).Maybe()
	mockRemote.On("GetObject", mock.Anything, entities.ZeroHash).Return(nil, entities.ErrNotExist).Maybe()

	store := &fakeCASStore{
		objects: make(map[entities.Hash][]byte),
	}
	p := &pusher{
		dirPath:        repoPath,
		pushedRefsFile: filepath.Join(repoPath, "pushed-refs"),
		shgit:          &shellgit.ShellGit{BinPath: "/usr/bin/git"},
		abfsClient:     mockRemote,
		store:          store,
		config: &ProjectConfig{
			Server:  "example.com",
			Project: "repo",
			FetchConfig: FetchConfig{
				FetchRefSpecs: []string{
					"+refs/heads/*:refs/remotes/origin/*",
					"^refs/changes/*/meta",
					"^refs/changes/*/robot-comments",
					"^refs/cache-automerge/*",
				},
				PackingStrategy: binding.PackingStrategyCommits,
			},
		},
		remoteName: "origin",
	}

	err = p.init()
	require.NoError(t, err)
	require.Equal(t, PushStateInitialized, p.state)
}
