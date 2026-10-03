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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/fastgit"
	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/shared/entities"
)

func TestFetchWithRetry(t *testing.T) {
	tempDir := t.TempDir()

	// Create a fake git executable to mock the shell command execution
	fakeGitPath := filepath.Join(tempDir, "fake_git")
	fakeGitScript := `#!/bin/bash
echo "$@" >> ` + filepath.Join(tempDir, "git_calls.log") + `

if [[ "$1" == "fetch" ]]; then
	if [[ -f ` + filepath.Join(tempDir, "fetch_error") + ` ]]; then
		err_msg=$(cat ` + filepath.Join(tempDir, "fetch_error") + `)
		echo "$err_msg" >&2
		exit 1
	fi
	exit 0
fi

if [[ "$1" == "gc" || "$1" == "pack-refs" ]]; then
	exit 0
fi
`
	err := os.WriteFile(fakeGitPath, []byte(fakeGitScript), 0755)
	if err != nil {
		t.Fatal(err)
	}

	repoPath := filepath.Join(tempDir, "repo.git")
	err = os.MkdirAll(repoPath, 0755)
	if err != nil {
		t.Fatal(err)
	}

	shgit := &shellgit.ShellGit{BinPath: fakeGitPath}

	resetLogs := func() {
		_ = os.Remove(filepath.Join(tempDir, "git_calls.log"))
		_ = os.Remove(filepath.Join(tempDir, "fetch_error"))
	}

	t.Run("Success on first try", func(t *testing.T) {
		resetLogs()

		err := fetchWithRetry(shgit, repoPath)
		if err != nil {
			t.Errorf("Expected nil error, got %v", err)
		}

		calls, _ := os.ReadFile(filepath.Join(tempDir, "git_calls.log"))
		if !strings.Contains(string(calls), "fetch") {
			t.Errorf("Expected fetch to be called, got: %s", string(calls))
		}
	})

	t.Run("Unhandled error gives up immediately", func(t *testing.T) {
		resetLogs()
		err := os.WriteFile(filepath.Join(tempDir, "fetch_error"), []byte("some random error"), 0644)
		require.NoError(t, err)

		err = fetchWithRetry(shgit, repoPath)
		if err == nil || !strings.Contains(err.Error(), "some random error") {
			t.Errorf("Expected random error, got %v", err)
		}

		calls, _ := os.ReadFile(filepath.Join(tempDir, "git_calls.log"))
		lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
		if len(lines) != 1 {
			t.Errorf("Expected exactly 1 call (no retries), got %d:\n%s", len(lines), string(calls))
		}
	})

	t.Run("Bad object refs recovers after failure", func(t *testing.T) {
		resetLogs()
		errFile := filepath.Join(tempDir, "fetch_error")
		err := os.WriteFile(errFile, []byte("fatal: bad object refs/changes/40/24674040/1"), 0644)
		require.NoError(t, err)

		go func() {
			// Remove the error file so the next retry succeeds
			time.Sleep(1 * time.Second)
			require.NoError(t, os.Remove(errFile))
		}()

		err = fetchWithRetry(shgit, repoPath)
		if err != nil {
			t.Errorf("Expected success after retry, got %v", err)
		}

		calls, _ := os.ReadFile(filepath.Join(tempDir, "git_calls.log"))
		callsStr := string(calls)
		if !strings.Contains(callsStr, "gc --prune=now") {
			t.Errorf("Expected gc to be called")
		}
		if !strings.Contains(callsStr, "pack-refs --all --prune") {
			t.Errorf("Expected pack-refs to be called")
		}
	})

	t.Run("Empty object file recovers after failure", func(t *testing.T) {
		resetLogs()
		errFile := filepath.Join(tempDir, "fetch_error")
		err = os.WriteFile(errFile, []byte("error: object file ./objects/fd/a82f4b070783098422d68b8896300eb56fc6cc is empty"), 0644)
		require.NoError(t, err)

		// Create a mock objects dir to test find command
		objectsPath := filepath.Join(repoPath, "objects")
		err = os.MkdirAll(objectsPath, 0755)
		require.NoError(t, err)

		go func() {
			// Remove the error file so the next retry succeeds
			time.Sleep(1 * time.Second)
			_ = os.Remove(errFile)
		}()

		err := fetchWithRetry(shgit, repoPath)
		if err != nil {
			t.Errorf("Expected success after retry, got %v", err)
		}
	})

	t.Run("Bad object refs fails twice and gives up", func(t *testing.T) {
		resetLogs()
		errFile := filepath.Join(tempDir, "fetch_error")
		err := os.WriteFile(errFile, []byte("fatal: bad object refs/changes/40/24674040/1"), 0644)
		require.NoError(t, err)

		err = fetchWithRetry(shgit, repoPath)
		if err == nil {
			t.Errorf("Expected error after two failures, got nil")
		}

		calls, _ := os.ReadFile(filepath.Join(tempDir, "git_calls.log"))
		lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
		// Expected calls:
		// 1. fetch
		// 2. gc
		// 3. pack-refs
		// 4. fetch
		if len(lines) != 4 {
			t.Errorf("Expected exactly 4 calls, got %d:\n%s", len(lines), string(calls))
		}
	})
}

func TestDagSyncObjectAccessor_ActuallyBlob(t *testing.T) {
	tempDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.RemoveAll(tempDir); err != nil {
			t.Errorf("os.RemoveAll(%s) failed: %v", tempDir, err)
		}
	})

	// Initialize a real git repo
	cmd := exec.Command("git", "init", "--bare")
	cmd.Dir = tempDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	// Create a blob in the git repo
	testContent := []byte("hello world")
	hashObjectCmd := exec.Command("git", "-c", "safe.bareRepository=all", "hash-object", "-w", "--stdin")
	hashObjectCmd.Dir = tempDir
	hashObjectCmd.Stdin = bytes.NewReader(testContent)
	var stderr bytes.Buffer
	hashObjectCmd.Stderr = &stderr
	out, err := hashObjectCmd.Output()
	if err != nil {
		t.Fatalf("git hash-object failed: %v, stderr: %s", err, stderr.String())
	}
	blobHashStr := strings.TrimSpace(string(out))
	blobHash := entities.NewHash(blobHashStr)

	repo, err := fastgit.Open(tempDir)
	if err != nil {
		t.Fatalf("fastgit.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	accessor := &dagSyncObjectAccessor{repo: repo}

	// GetObject on a blob should return a NotExist error
	ctx := context.Background()
	_, err = accessor.GetObject(ctx, blobHash)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !entities.IsNotExist(err) {
		t.Errorf("expected NotExist error, got: %v", err)
	}

	// OpenBlob on a blob should succeed
	r, err := accessor.OpenBlob(ctx, blobHash)
	if err != nil {
		t.Fatalf("OpenBlob failed: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	size, err := r.Size()
	if err != nil {
		t.Fatalf("Size failed: %v", err)
	}
	if size != int64(len(testContent)) {
		t.Errorf("expected size %d, got %d", len(testContent), size)
	}

	content, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(content, testContent) {
		t.Errorf("expected content %q, got %q", testContent, content)
	}
}

func TestEnsureGitRepo_NegativeRefspec(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "repo.git")

	// 1. Initial creation of bare git repo
	err := ensureGitRepo(repoPath)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(repoPath, "HEAD"))

	// 2. Write a git config with negative refspecs (which would fail go-git PlainOpen in v5.19+)
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

	// 3. Re-ensuring the existing repo must succeed without errors
	err = ensureGitRepo(repoPath)
	require.NoError(t, err)
}

func TestFetchWithNegativeRefspecs(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "all")

	tempDir := t.TempDir()
	upstreamDir := filepath.Join(tempDir, "upstream.git")
	downstreamDir := filepath.Join(tempDir, "downstream.git")
	worktreeDir := filepath.Join(tempDir, "worktree")

	runGit := func(dir string, args ...string) {
		cmdArgs := append([]string{"-c", "safe.bareRepository=all"}, args...)
		cmd := exec.Command("git", cmdArgs...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, string(out))
	}

	// 1. Setup upstream bare repository
	require.NoError(t, os.MkdirAll(upstreamDir, 0755))
	runGit(upstreamDir, "init", "--bare")

	// 2. Populate upstream with branches and excluded refs
	require.NoError(t, os.MkdirAll(worktreeDir, 0755))
	runGit(worktreeDir, "init", "-b", "main")
	runGit(worktreeDir, "config", "user.name", "Test")
	runGit(worktreeDir, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(worktreeDir, "file.txt"), []byte("data"), 0644))
	runGit(worktreeDir, "add", "file.txt")
	runGit(worktreeDir, "commit", "-m", "init")
	runGit(worktreeDir, "remote", "add", "origin", upstreamDir)
	runGit(worktreeDir, "push", "origin", "HEAD:refs/heads/main")
	runGit(worktreeDir, "push", "origin", "HEAD:refs/heads/feature")
	runGit(worktreeDir, "push", "origin", "HEAD:refs/changes/01/1001/meta")
	runGit(worktreeDir, "push", "origin", "HEAD:refs/changes/01/1001/robot-comments")
	runGit(worktreeDir, "push", "origin", "HEAD:refs/cache-automerge/auto1")

	// 3. Initialize downstream bare repository using ensureGitRepo
	require.NoError(t, ensureGitRepo(downstreamDir))

	// 4. Write config with negative refspecs
	configContent := fmt.Sprintf(`[core]
	repositoryformatversion = 0
	filemode = true
	bare = true
[remote "origin"]
	url = %s
	fetch = +refs/heads/*:refs/remotes/origin/*
	fetch = ^refs/changes/*/meta
	fetch = ^refs/changes/*/robot-comments
	fetch = ^refs/cache-automerge/*
`, upstreamDir)
	require.NoError(t, os.WriteFile(filepath.Join(downstreamDir, "config"), []byte(configContent), 0644))

	// 5. Run fetch using ShellGit
	shgit := shellgit.NewShellGit("/usr/bin/git")
	require.NoError(t, shgit.Fetch(downstreamDir))

	// 6. Verify fetched refs
	refs, err := shgit.ShowRefs(downstreamDir)
	require.NoError(t, err)

	hasMain := false
	hasFeature := false
	for ref := range refs {
		if strings.Contains(ref, "refs/remotes/origin/main") {
			hasMain = true
		}
		if strings.Contains(ref, "refs/remotes/origin/feature") {
			hasFeature = true
		}
		require.False(t, strings.Contains(ref, "meta"), "negative ref meta should not be fetched: %s", ref)
		require.False(t, strings.Contains(ref, "robot-comments"), "negative ref robot-comments should not be fetched: %s", ref)
		require.False(t, strings.Contains(ref, "cache-automerge"), "negative ref cache-automerge should not be fetched: %s", ref)
	}
	require.True(t, hasMain, "expected refs/remotes/origin/main to be fetched")
	require.True(t, hasFeature, "expected refs/remotes/origin/feature to be fetched")

	// 7. Verify subsequent ensureGitRepo on the populated repo succeeds
	require.NoError(t, ensureGitRepo(downstreamDir))
}
