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

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestParseFetch_SingleLine(t *testing.T) {
	require := require.New(t)

	fakeInput := "* 0000000000000000000000000000000000000000 5898b72344c8eb0e0e88b2419d775864be6bfa84 refs/tags/mirror-upstream"
	refs, err := parseFetchPorcelainOutput(strings.NewReader(fakeInput))
	require.NoError(err)
	require.Len(refs, 1)
	ref := refs[0]
	require.Equal("*", ref.StatusFlag)
	require.Equal("0000000000000000000000000000000000000000", ref.OldObjectId.String())
	require.Equal("5898b72344c8eb0e0e88b2419d775864be6bfa84", ref.NewObjectId.String())
	require.Equal("refs/tags/mirror-upstream", ref.LocalReference)
}

func TestParseFetch_SpaceFlag(t *testing.T) {
	require := require.New(t)

	fakeInput := "  0000000000000000000000000000000000000000 5898b72344c8eb0e0e88b2419d775864be6bfa84 refs/tags/mirror-upstream"
	refs, err := parseFetchPorcelainOutput(strings.NewReader(fakeInput))
	require.NoError(err)
	require.Len(refs, 1)
	ref := refs[0]
	require.Equal(" ", ref.StatusFlag)
	require.Equal("0000000000000000000000000000000000000000", ref.OldObjectId.String())
	require.Equal("5898b72344c8eb0e0e88b2419d775864be6bfa84", ref.NewObjectId.String())
	require.Equal("refs/tags/mirror-upstream", ref.LocalReference)
}

func TestParseFetch_MultipleLines(t *testing.T) {
	require := require.New(t)

	fakeInput := `  0000000000000000000000000000000000000000 5898b72344c8eb0e0e88b2419d775864be6bfa84 refs/tags/mirror-upstream
* 5898b72344c8eb0e0e88b2419d775864be6bfa84 dd464f319fb6e82f981e26fed3b76482f15bd746 refs/heads/release
- c3c8d20dc1b302a3ac16fe62c3302f1d0c5b6182 afc0e77b66b9e663e3d246f9e61d5ea8c91d131d refs/changes/91/210391/1
`
	refs, err := parseFetchPorcelainOutput(strings.NewReader(fakeInput))
	require.NoError(err)
	require.Len(refs, 3)
	require.Equal(&FetchedRef{
		StatusFlag:     " ",
		OldObjectId:    plumbing.ZeroHash,
		NewObjectId:    plumbing.NewHash("5898b72344c8eb0e0e88b2419d775864be6bfa84"),
		LocalReference: "refs/tags/mirror-upstream",
	}, refs[0])
	require.Equal(&FetchedRef{
		StatusFlag:     "*",
		OldObjectId:    plumbing.NewHash("5898b72344c8eb0e0e88b2419d775864be6bfa84"),
		NewObjectId:    plumbing.NewHash("dd464f319fb6e82f981e26fed3b76482f15bd746"),
		LocalReference: "refs/heads/release",
	}, refs[1])
	require.Equal(&FetchedRef{
		StatusFlag:     "-",
		OldObjectId:    plumbing.NewHash("c3c8d20dc1b302a3ac16fe62c3302f1d0c5b6182"),
		NewObjectId:    plumbing.NewHash("afc0e77b66b9e663e3d246f9e61d5ea8c91d131d"),
		LocalReference: "refs/changes/91/210391/1",
	}, refs[2])
}

func TestParseFetch_BadFormat(t *testing.T) {
	require := require.New(t)

	fakeInput := "badinput"
	_, err := parseFetchPorcelainOutput(strings.NewReader(fakeInput))
	require.Error(err)
}

func TestParseFetch_PartialLine(t *testing.T) {
	require := require.New(t)

	fakeInput := "* 0000000000000000000000000000000000000000"
	_, err := parseFetchPorcelainOutput(strings.NewReader(fakeInput))
	require.Error(err)
}

func TestParseLongListTree(t *testing.T) {
	require := require.New(t)

	// Test regular file
	line := "100644 blob e69de29bb2d1d6434b8b29ae775ad8c2e48c5391 12345\tfoo/bar/baz.txt"
	ent, err := parseLongListTree(line)
	require.NoError(err)
	require.Equal("foo/bar/baz.txt", ent.Name)
	require.Equal(entities.FileModeRegular, ent.Mode)
	require.Equal(entities.EmptyFileHash, ent.Hash)
	require.Equal(int64(12345), ent.Size)

	// Test tree (directory) with size "-"
	line = "040000 tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904 -\tfoo/bar"
	ent, err = parseLongListTree(line)
	require.NoError(err)
	require.Equal("foo/bar", ent.Name)
	require.Equal(entities.FileModeDir, ent.Mode)
	require.Equal(entities.EmptyDirHash, ent.Hash)
	require.Equal(int64(0), ent.Size)

	// Test path with spaces
	line = "100755 blob e69de29bb2d1d6434b8b29ae775ad8c2e48c5391 99\tfoo/bar/my file name.sh"
	ent, err = parseLongListTree(line)
	require.NoError(err)
	require.Equal("foo/bar/my file name.sh", ent.Name)
	require.Equal(entities.FileModeExecutable, ent.Mode)
	require.Equal(entities.EmptyFileHash, ent.Hash)
	require.Equal(int64(99), ent.Size)

	// Test bad format
	_, err = parseLongListTree("badformat")
	require.Error(err)

	// Test invalid hash
	_, err = parseLongListTree("100644 blob invalidhash 123\tfoo.txt")
	require.Error(err)

	// Test invalid mode
	_, err = parseLongListTree("999999 blob e69de29bb2d1d6434b8b29ae775ad8c2e48c5391 123\tfoo.txt")
	require.Error(err)

	// Test invalid size
	_, err = parseLongListTree("100644 blob e69de29bb2d1d6434b8b29ae775ad8c2e48c5391 badsize\tfoo.txt")
	require.Error(err)
}

func TestGitCloneBareWithWorktree(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	shgit := NewShellGit("")

	// Create a source repo
	srcDir := t.TempDir()
	_, err := shgit.runGitCommand(srcDir, "init")
	require.NoError(err)
	_, err = shgit.runGitCommand(srcDir, "config", "user.name", "Test User")
	require.NoError(err)
	_, err = shgit.runGitCommand(srcDir, "config", "user.email", "test@example.com")
	require.NoError(err)
	_, err = shgit.runGitCommand(srcDir, "commit", "--allow-empty", "-m", "initial commit")
	require.NoError(err)

	// Target bare gitDir and workTree in nested non-existent paths
	baseDir := t.TempDir()
	gitDir := filepath.Join(baseDir, "nested", "gitdir.git")
	workTree := filepath.Join(baseDir, "nested", "worktree")

	err = shgit.GitCloneBareWithWorktree(ctx, gitDir, workTree, srcDir, "origin", "HEAD")
	require.NoError(err)

	// Verify core.bare and core.worktree config
	bareVal, err := shgit.GetConfig(gitDir, "core.bare", "")
	require.NoError(err)
	require.Equal("false", bareVal)

	workTreeAbs, err := filepath.Abs(workTree)
	require.NoError(err)

	worktreeVal, err := shgit.GetConfig(gitDir, "core.worktree", "")
	require.NoError(err)
	require.Equal(workTreeAbs, worktreeVal)

	// Verify .git symlink in worktree points to gitDir relatively
	dotGitFile := filepath.Join(workTree, ".git")
	target, err := os.Readlink(dotGitFile)
	require.NoError(err)
	expectedRel, err := filepath.Rel(workTree, gitDir)
	require.NoError(err)
	require.Equal(expectedRel, target)

	// Verify git status runs cleanly in workTree
	statusOut, err := shgit.runGitCommand(workTree, "status", "--porcelain")
	require.NoError(err)
	require.Empty(strings.TrimSpace(statusOut))
}

func TestConfigWithNonGitConfigFileOrDir(t *testing.T) {
	require := require.New(t)
	shgit := NewShellGit("")

	t.Run("repo with config directory", func(t *testing.T) {
		repoDir := t.TempDir()
		_, err := shgit.runGitCommand(repoDir, "init")
		require.NoError(err)

		// Create a directory named "config" in repo root
		configDir := filepath.Join(repoDir, "config")
		require.NoError(os.Mkdir(configDir, 0755))
		require.NoError(os.WriteFile(filepath.Join(configDir, "something.txt"), []byte("data"), 0644))

		// AddConfig, GetConfig, GetAllConfigs should succeed using the git dir
		err = shgit.AddConfig(repoDir, "custom.testkey", "testval1")
		require.NoError(err)

		val, err := shgit.GetConfig(repoDir, "custom.testkey", "")
		require.NoError(err)
		require.Equal("testval1", val)

		vals, err := shgit.GetAllConfigs(repoDir, "custom.testkey")
		require.NoError(err)
		require.Equal([]string{"testval1"}, vals)
	})

	t.Run("repo with non-git regular config file", func(t *testing.T) {
		repoDir := t.TempDir()
		_, err := shgit.runGitCommand(repoDir, "init")
		require.NoError(err)

		// Create a regular file named "config" in repo root
		configFile := filepath.Join(repoDir, "config")
		require.NoError(os.WriteFile(configFile, []byte("NOT_A_GIT_CONFIG\n"), 0644))

		err = shgit.AddConfig(repoDir, "custom.filekey", "testval2")
		require.NoError(err)

		val, err := shgit.GetConfig(repoDir, "custom.filekey", "")
		require.NoError(err)
		require.Equal("testval2", val)

		// Ensure non-git config file was not overwritten or corrupted
		content, err := os.ReadFile(configFile)
		require.NoError(err)
		require.Equal("NOT_A_GIT_CONFIG\n", string(content))
	})

	t.Run("bare repo", func(t *testing.T) {
		bareDir := t.TempDir()
		_, err := shgit.runGitCommand(bareDir, "init", "--bare")
		require.NoError(err)

		err = shgit.AddConfig(bareDir, "custom.barekey", "bareval")
		require.NoError(err)

		val, err := shgit.GetConfig(bareDir, "custom.barekey", "")
		require.NoError(err)
		require.Equal("bareval", val)

		vals, err := shgit.GetAllConfigs(bareDir, "custom.barekey")
		require.NoError(err)
		require.Equal([]string{"bareval"}, vals)
	})
}
