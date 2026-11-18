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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers/inmemory"
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

if [[ "$*" == *"cat-file"* ]]; then
	while read -r line; do echo "commit"; done
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

if [[ "$*" == *"cat-file"* ]]; then
	while read -r line; do echo "commit"; done
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

// blockingServer blocks the upload of one object until the context of the
// call is done.
type blockingServer struct {
	*inmemory.InMemoryServer
	block   entities.Hash
	blocked chan struct{}
}

func (s *blockingServer) PutObject(ctx context.Context, key entities.Hash, reader remote.ContentReadCloser) error {
	if key == s.block {
		close(s.blocked)
		<-ctx.Done()
		_ = reader.Close()
		return ctx.Err()
	}
	return s.InMemoryServer.PutObject(ctx, key, reader)
}

// newUpstreamRepo creates a bare repository with one commit on main that
// adds a file with the given content, and returns its path and the commit.
func newUpstreamRepo(t *testing.T, content []byte) (string, entities.Hash) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "all")

	tempDir := t.TempDir()
	upstreamDir := filepath.Join(tempDir, "upstream.git")
	worktreeDir := filepath.Join(tempDir, "worktree")

	runGit := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, string(out))
		return strings.TrimSpace(string(out))
	}

	require.NoError(t, os.MkdirAll(upstreamDir, 0755))
	runGit(upstreamDir, "init", "--bare")
	require.NoError(t, os.MkdirAll(worktreeDir, 0755))
	runGit(worktreeDir, "init", "-b", "main")
	runGit(worktreeDir, "config", "user.name", "Test")
	runGit(worktreeDir, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(worktreeDir, "file.txt"), content, 0644))
	runGit(worktreeDir, "add", "file.txt")
	runGit(worktreeDir, "commit", "-m", "init")
	runGit(worktreeDir, "push", upstreamDir, "HEAD:refs/heads/main")
	return upstreamDir, entities.NewHash(runGit(worktreeDir, "rev-parse", "HEAD"))
}

func newTestPusher(dirPath, url string, client remote.RemoteCloser) *pusher {
	return &pusher{
		config: &ProjectConfig{
			Server:  "example.com",
			Project: "repo",
			URL:     url,
			FetchConfig: FetchConfig{
				FetchRefSpecs:    []string{"+refs/heads/*:refs/heads/*"},
				Workers:          2,
				PackPushInterval: 24 * time.Hour,
				PackingStrategy:  binding.PackingStrategyCommits,
			},
		},
		remoteName:     defaultRemoteName,
		abfsClient:     client,
		store:          remote.NewRemoteStore(client),
		dirPath:        dirPath,
		pushedRefsFile: filepath.Join(dirPath, pushedRefsFile),
		state:          PushStateCreated,
		shgit:          &shellgit.ShellGit{BinPath: "git"},
		deepTreeMap:    make(map[entities.Hash]entities.Hash),
	}
}

func TestPush_CancelledDuringUpload(t *testing.T) {
	content := []byte("content blocked during upload")
	upstreamDir, head := newUpstreamRepo(t, content)
	dirPath := filepath.Join(t.TempDir(), "repo.git")

	server := inmemory.New()
	blocking := &blockingServer{
		InMemoryServer: server,
		block:          objects.HashBlobBytes(content),
		blocked:        make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	pushErr := make(chan error, 1)
	go func() {
		pushErr <- newTestPusher(dirPath, upstreamDir, blocking).Push(ctx)
	}()
	select {
	case <-blocking.blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("the upload was not attempted")
	}
	cancel()
	require.ErrorIs(t, <-pushErr, context.Canceled)

	pushedRefs, err := readPushedRefs(filepath.Join(dirPath, pushedRefsFile))
	require.NoError(t, err)
	require.Empty(t, pushedRefs)
	refs, err := server.GetRefs(context.Background(), "example.com", "repo", []string{"refs/heads/main"})
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, refs[0])

	require.NoError(t, newTestPusher(dirPath, upstreamDir, server).Push(context.Background()))
	refs, err = server.GetRefs(context.Background(), "example.com", "repo", []string{"refs/heads/main"})
	require.NoError(t, err)
	require.Equal(t, head, refs[0])
}

// blockingRefsServer blocks PutRefs calls for branches until the context of
// the call is done. Like the server, it first rejects refs to objects it does
// not hold.
type blockingRefsServer struct {
	*inmemory.InMemoryServer
	blocked chan struct{}
}

func (s *blockingRefsServer) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	if len(refs) == 0 || !strings.HasPrefix(refs[0].Ref, "refs/heads/") {
		return s.InMemoryServer.PutRefs(ctx, server, project, refs)
	}
	var misses []entities.Hash
	for _, ref := range refs {
		headers, err := s.Headers(ctx, []entities.Hash{ref.Hash})
		if err != nil {
			return err
		}
		if !ref.Hash.IsZero() && headers[0].IsEmpty() {
			misses = append(misses, ref.Hash)
		}
	}
	if len(misses) > 0 {
		return &remote.ErrObjectLinkValidation{MissingLinks: misses}
	}
	close(s.blocked)
	<-ctx.Done()
	return ctx.Err()
}

func TestPush_CancelledDuringPutRefs(t *testing.T) {
	content := []byte("content uploaded before the refs")
	upstreamDir, _ := newUpstreamRepo(t, content)
	dirPath := filepath.Join(t.TempDir(), "repo.git")
	server := inmemory.New()
	blocking := &blockingRefsServer{InMemoryServer: server, blocked: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	pushErr := make(chan error, 1)
	go func() {
		pushErr <- newTestPusher(dirPath, upstreamDir, blocking).Push(ctx)
	}()
	select {
	case <-blocking.blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("the refs were not sent")
	}
	headers, err := server.Headers(context.Background(), []entities.Hash{objects.HashBlobBytes(content)})
	require.NoError(t, err)
	require.False(t, headers[0].IsEmpty(), "the blob was not uploaded before the refs")
	cancel()
	require.ErrorIs(t, <-pushErr, context.Canceled)

	pushedRefs, err := readPushedRefs(filepath.Join(dirPath, pushedRefsFile))
	require.NoError(t, err)
	require.Empty(t, pushedRefs)
	refs, err := server.GetRefs(context.Background(), "example.com", "repo", []string{"refs/heads/main"})
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, refs[0])
}

func TestPush_CancelledContext(t *testing.T) {
	upstreamDir, _ := newUpstreamRepo(t, []byte("content of a cancelled push"))
	dirPath := filepath.Join(t.TempDir(), "repo.git")
	server := inmemory.New()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, newTestPusher(dirPath, upstreamDir, server).Push(ctx), context.Canceled)

	pushedRefs, err := readPushedRefs(filepath.Join(dirPath, pushedRefsFile))
	require.NoError(t, err)
	require.Empty(t, pushedRefs)
	refs, err := server.GetRefs(context.Background(), "example.com", "repo", []string{"refs/heads/main"})
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, refs[0])
}

func TestPush_PushedRefsMissingOnServer(t *testing.T) {
	upstreamDir, head := newUpstreamRepo(t, []byte("content recorded but never sent"))
	dirPath := filepath.Join(t.TempDir(), "repo.git")
	server := inmemory.New()

	require.NoError(t, ensureGitRepo(dirPath))
	require.NoError(t, writePushedRefs(filepath.Join(dirPath, pushedRefsFile), map[string]entities.Hash{"refs/heads/main": head}))

	require.NoError(t, newTestPusher(dirPath, upstreamDir, server).Push(context.Background()))
	refs, err := server.GetRefs(context.Background(), "example.com", "repo", []string{"refs/heads/main"})
	require.NoError(t, err)
	require.Equal(t, head, refs[0])
}

type countingServer struct {
	*inmemory.InMemoryServer
	getRefsCalls int
	maxNames     int
}

func (s *countingServer) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	s.getRefsCalls++
	s.maxNames = max(s.maxNames, len(refs))
	return s.InMemoryServer.GetRefs(ctx, server, project, refs)
}

type refsServer struct {
	remote.RemoteCloser
	refs  map[string]entities.Hash
	err   error
	short bool
}

func (s *refsServer) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	if s.err != nil {
		return nil, s.err
	}
	hashes := make([]entities.Hash, len(refs))
	for i, ref := range refs {
		hashes[i] = s.refs[ref]
	}
	if s.short {
		hashes = hashes[:len(hashes)-1]
	}
	return hashes, nil
}

func TestCheckPushedRefs(t *testing.T) {
	hashA := entities.NewHash("1111111111111111111111111111111111111111")
	hashB := entities.NewHash("2222222222222222222222222222222222222222")
	hashC := entities.NewHash("3333333333333333333333333333333333333333")
	recorded := map[string]entities.Hash{
		"refs/heads/a": hashA,
		"refs/heads/b": hashB,
		"refs/tags/c":  hashC,
	}

	tests := []struct {
		name          string
		server        map[string]entities.Hash
		err           error
		short         bool
		wantErr       bool
		want          map[string]entities.Hash
		wantCorrected map[string]entities.Hash
	}{
		{
			name:   "AllHeld",
			server: recorded,
			want:   recorded,
		},
		{
			name:          "MissingDroppedAndDifferentCorrected",
			server:        map[string]entities.Hash{"refs/heads/a": hashA, "refs/heads/b": hashC},
			want:          map[string]entities.Hash{"refs/heads/a": hashA, "refs/heads/b": hashC},
			wantCorrected: map[string]entities.Hash{"refs/heads/b": hashC},
		},
		{
			name: "NotExistDropsAll",
			err:  entities.ErrNotExist,
			want: map[string]entities.Hash{},
		},
		{
			name:    "OtherErrorKeepsFile",
			err:     errors.New("connection refused"),
			wantErr: true,
			want:    recorded,
		},
		{
			name:    "ShortReplyKeepsFile",
			server:  recorded,
			short:   true,
			wantErr: true,
			want:    recorded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirPath := filepath.Join(t.TempDir(), "repo.git")
			require.NoError(t, ensureGitRepo(dirPath))
			fileName := filepath.Join(dirPath, pushedRefsFile)
			require.NoError(t, writePushedRefs(fileName, recorded))

			p := newTestPusher(dirPath, "", &refsServer{refs: tt.server, err: tt.err, short: tt.short})

			err := p.checkPushedRefs(context.Background())
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			got, err := readPushedRefs(fileName)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			if len(tt.wantCorrected) > 0 {
				require.Equal(t, tt.wantCorrected, p.correctedRefs)
			}
		})
	}
}

func TestCheckPushedRefs_Chunked(t *testing.T) {
	dirPath := t.TempDir()
	fileName := filepath.Join(dirPath, pushedRefsFile)
	server := &countingServer{InMemoryServer: inmemory.New()}

	hash := objects.HashBlobBytes([]byte("chunked"))
	require.NoError(t, server.PutObject(context.Background(), hash, remote.ContentReaderFromBytes([]byte("blob 7\x00chunked"))))
	recorded := make(map[string]entities.Hash)
	refHashes := make([]entities.RefHash, 0)
	for i := range 2*pushedRefsCheckChunkSize + 1 {
		ref := fmt.Sprintf("refs/changes/%d", i)
		recorded[ref] = hash
		if i != 0 {
			refHashes = append(refHashes, entities.RefHash{Ref: ref, Hash: hash})
		}
	}
	require.NoError(t, server.PutRefs(context.Background(), "example.com", "repo", refHashes))
	require.NoError(t, writePushedRefs(fileName, recorded))

	require.NoError(t, newTestPusher(dirPath, "", server).checkPushedRefs(context.Background()))
	require.Equal(t, 3, server.getRefsCalls)
	require.Equal(t, pushedRefsCheckChunkSize, server.maxNames)
	got, err := readPushedRefs(fileName)
	require.NoError(t, err)
	delete(recorded, "refs/changes/0")
	require.Equal(t, recorded, got)
}

// failingRefsServer fails GetRefs calls for branches while fail is set.
type failingRefsServer struct {
	*inmemory.InMemoryServer
	fail bool
}

func (s *failingRefsServer) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	if s.fail && slices.ContainsFunc(refs, func(ref string) bool { return strings.HasPrefix(ref, "refs/heads/") }) {
		return nil, errors.New("connection refused")
	}
	return s.InMemoryServer.GetRefs(ctx, server, project, refs)
}

func TestPush_PushedRefsCheckRetried(t *testing.T) {
	upstreamDir, head := newUpstreamRepo(t, []byte("content checked on a later push"))
	dirPath := filepath.Join(t.TempDir(), "repo.git")
	server := &failingRefsServer{InMemoryServer: inmemory.New(), fail: true}

	require.NoError(t, ensureGitRepo(dirPath))
	require.NoError(t, writePushedRefs(filepath.Join(dirPath, pushedRefsFile), map[string]entities.Hash{"refs/heads/main": head}))

	p := newTestPusher(dirPath, upstreamDir, server)
	require.NoError(t, p.Push(context.Background()))
	require.False(t, p.pushedRefsChecked)

	server.fail = false
	require.NoError(t, p.Push(context.Background()))
	require.True(t, p.pushedRefsChecked)
	refs, err := server.GetRefs(context.Background(), "example.com", "repo", []string{"refs/heads/main"})
	require.NoError(t, err)
	require.Equal(t, head, refs[0])
}

func TestPush_PushedRefsDifferOnServer(t *testing.T) {
	// The ref is deleted upstream before the first fetch after the restart, so
	// only the corrected pushed-refs entry can make the push delete it.
	tests := []struct {
		name           string
		serverHasLocal bool
	}{
		{name: "ServerHashKnownLocally", serverHasLocal: true},
		{name: "ServerHashUnknownLocally"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstreamDir, head := newUpstreamRepo(t, []byte("content of a deleted branch"))
			gitCmd := exec.Command("git", "update-ref", "refs/heads/x", head.String())
			gitCmd.Dir = upstreamDir
			out, err := gitCmd.CombinedOutput()
			require.NoError(t, err, string(out))

			dirPath := filepath.Join(t.TempDir(), "repo.git")
			server := inmemory.New()
			require.NoError(t, newTestPusher(dirPath, upstreamDir, server).Push(context.Background()))

			if !tt.serverHasLocal {
				content := []byte("not in the local repository")
				hash := objects.HashBlobBytes(content)
				require.NoError(t, server.PutObject(context.Background(), hash, remote.ContentReaderFromBytes(fmt.Appendf(nil, "blob %d\x00%s", len(content), content))))
				require.NoError(t, server.PutRefs(context.Background(), "example.com", "repo", []entities.RefHash{{Ref: "refs/heads/x", Hash: hash}}))
			}
			// pushed-refs records a hash the server does not hold for x.
			require.NoError(t, writePushedRefs(filepath.Join(dirPath, pushedRefsFile), map[string]entities.Hash{
				"refs/heads/main": head,
				"refs/heads/x":    entities.NewHash("2222222222222222222222222222222222222222"),
			}))

			gitCmd = exec.Command("git", "update-ref", "-d", "refs/heads/x")
			gitCmd.Dir = upstreamDir
			out, err = gitCmd.CombinedOutput()
			require.NoError(t, err, string(out))

			require.NoError(t, newTestPusher(dirPath, upstreamDir, server).Push(context.Background()))
			refs, err := server.GetRefs(context.Background(), "example.com", "repo", []string{"refs/heads/main", "refs/heads/x"})
			require.NoError(t, err)
			require.Equal(t, []entities.Hash{head, entities.ZeroHash}, refs)
		})
	}
}

func TestPush_PushedRefsUnknownAfterRestart(t *testing.T) {
	// A previous pusher took x from the server into pushed-refs and stopped
	// before its next push. The local repository never had the hash, and
	// upstream no longer has x.
	upstreamDir, head := newUpstreamRepo(t, []byte("content of a restarted pusher"))
	dirPath := filepath.Join(t.TempDir(), "repo.git")
	server := inmemory.New()
	require.NoError(t, newTestPusher(dirPath, upstreamDir, server).Push(context.Background()))

	content := []byte("not in the local repository")
	hash := objects.HashBlobBytes(content)
	require.NoError(t, server.PutObject(context.Background(), hash, remote.ContentReaderFromBytes(fmt.Appendf(nil, "blob %d\x00%s", len(content), content))))
	require.NoError(t, server.PutRefs(context.Background(), "example.com", "repo", []entities.RefHash{{Ref: "refs/heads/x", Hash: hash}}))
	fileName := filepath.Join(dirPath, pushedRefsFile)
	require.NoError(t, writePushedRefs(fileName, map[string]entities.Hash{
		"refs/heads/main": head,
		"refs/heads/x":    hash,
	}))

	require.NoError(t, newTestPusher(dirPath, upstreamDir, server).Push(context.Background()))
	refs, err := server.GetRefs(context.Background(), "example.com", "repo", []string{"refs/heads/main", "refs/heads/x"})
	require.NoError(t, err)
	require.Equal(t, []entities.Hash{head, entities.ZeroHash}, refs)
	got, err := readPushedRefs(fileName)
	require.NoError(t, err)
	require.Equal(t, map[string]entities.Hash{"refs/heads/main": head}, got)
}

// noMapsServer drops map results, as a push stopped before it saved its tree
// helper objects leaves them missing.
type noMapsServer struct {
	*inmemory.InMemoryServer
}

func (s *noMapsServer) AddMapResult(ctx context.Context, from entities.Hash, to entities.Hash) error {
	return nil
}

func TestPush_CorrectedRefHelperObjects(t *testing.T) {
	upstreamDir, head := newUpstreamRepo(t, []byte("content of a corrected branch"))
	dirPath := filepath.Join(t.TempDir(), "repo.git")
	server := inmemory.New()
	require.NoError(t, newTestPusher(dirPath, upstreamDir, &noMapsServer{InMemoryServer: server}).Push(context.Background()))

	gitCmd := exec.Command("git", "rev-parse", head.String()+"^{tree}")
	gitCmd.Dir = upstreamDir
	out, err := gitCmd.Output()
	require.NoError(t, err)
	request := &binding.DeepTreeRequest{
		Version: binding.CurrentDeepTreeRequestVersion,
		Tree:    entities.NewHash(strings.TrimSpace(string(out))),
	}
	require.NoError(t, request.Encode())
	results, err := server.MapResults(context.Background(), request.Hash)
	require.NoError(t, err)
	require.Empty(t, results)

	// pushed-refs records a hash the server does not hold for main.
	require.NoError(t, writePushedRefs(filepath.Join(dirPath, pushedRefsFile), map[string]entities.Hash{
		"refs/heads/main": entities.NewHash("2222222222222222222222222222222222222222"),
	}))

	p := newTestPusher(dirPath, upstreamDir, server)
	require.NoError(t, p.Push(context.Background()))
	results, err = server.MapResults(context.Background(), request.Hash)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Nil(t, p.correctedRefs)
}

func TestPresentHashes_ShortReply(t *testing.T) {
	tempDir := t.TempDir()
	fakeGitPath := filepath.Join(tempDir, "fake_git")
	require.NoError(t, os.WriteFile(fakeGitPath, []byte("#!/bin/bash\ncat >/dev/null\necho commit\n"), 0755))

	p := &pusher{
		dirPath: tempDir,
		shgit:   &shellgit.ShellGit{BinPath: fakeGitPath},
	}
	_, err := p.presentHashes([]entities.Hash{
		entities.NewHash("1111111111111111111111111111111111111111"),
		entities.NewHash("2222222222222222222222222222222222222222"),
	})
	require.Error(t, err)
}
