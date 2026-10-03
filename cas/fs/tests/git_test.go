// Copyright 2025 Google LLC
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

package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/abfs/cas/fs/gitindex"
	"github.com/stretchr/testify/require"
)

/*
 * test control interface
 */
var nonCasGitHead string

func gitTestInit(nonCasTestRoot, objectsDir string) error {

	baseline := filepath.Join(nonCasTestRoot, "baseline")
	if err := env.git.CreateNonCasRepo(baseline); err != nil {
		return fmt.Errorf("failed creating non-cas repo %v", err)
	}
	// Copy objects into our object directory for tests
	if err := TxObjects(baseline, objectsDir); err != nil {
		return fmt.Errorf("failed to tx cobjects %v", err)
	}
	head, err := env.git.GetHead(baseline)
	if err != nil {
		return fmt.Errorf("failed to get head %v", err)
	}
	nonCasGitHead = head
	return nil
}

// Setup git via control
func TestGit_Control(t *testing.T) {
	path := env.GitTestNextPath()
	err := env.git.CtrlGitTest(path, nonCasGitHead)
	require.NoError(t, err)
}
func TestGit_ControlRm(t *testing.T) {
	path := env.GitTestNextPath()
	err := env.git.CtrlGitRmTest(path, nonCasGitHead)
	require.NoError(t, err)
}

// Consider deprecating if no longer in use
func TestGit_ControlObj(t *testing.T) {
	path := env.GitTestNextPath()
	// give a different name than objects to test sw
	err := env.git.CtrlGitObjTest(path, "git_objects")
	require.NoError(t, err)
}
func TestGit_ControlObjRm(t *testing.T) {
	path := env.GitTestNextPath()
	// give a different name than objects to test sw
	err := env.git.CtrlGitObjRmTest(path, "git_objects")
	require.NoError(t, err)
}
func TestGit_Head(t *testing.T) {
	path := env.GitTestNextPath()
	err := env.git.HeadTest(path, nonCasGitHead)
	require.NoError(t, err)
}
func TestGit_Show(t *testing.T) {
	path := env.GitTestNextPath()
	err := env.git.ShowTest(path, nonCasGitHead)
	require.NoError(t, err)
}
func TestGit_Commit(t *testing.T) {
	path := env.GitTestNextPath()
	err := env.git.CommitTest(path, nonCasGitHead)
	require.NoError(t, err)
}

func TestGit_Tree(t *testing.T) {
	srcPath := env.GitTestNextPath()
	err := MkdirTest(srcPath)
	require.NoError(t, err)

	// Mark .git as excluded from CAS tracking
	gitDir := filepath.Join(srcPath, ".git")
	err = MkdirTest(gitDir)
	require.NoError(t, err)

	err = env.cas.fs.SetExcluded(gitDir, true)
	require.NoError(t, err)

	err = env.git.PopulateNonCasRepo(srcPath)
	require.NoError(t, err)

	// Get the tree hash and tx the objects
	err = TxObjects(srcPath, env.mountObjDir)
	require.NoError(t, err)

	head, err := env.git.GetHead(srcPath)
	require.NoError(t, err)
	hash, err := env.cas.fs.Hash(srcPath)
	require.NoError(t, err)

	// Create new directory with the git data
	path := env.GitTestNextPath()
	err = MkdirTest(path)
	require.NoError(t, err)
	err = env.cas.fs.SetHash(path, hash)
	require.NoError(t, err)
	// setup the git
	err = env.git.fs.CreateGit(path, ".git", head)
	require.NoError(t, err)

	indexBefore, err := os.ReadFile(filepath.Join(path, ".git", "index"))
	require.NoError(t, err)
	_, err = env.git.GitStatus(path)
	require.NoError(t, err)

	indexAfter, err := os.ReadFile(filepath.Join(path, ".git", "index"))
	require.NoError(t, err)
	require.Equal(t, indexBefore, indexAfter)

}

func TestGit_LFSLikeAccess(t *testing.T) {
	path := env.GitTestNextPath()
	err := env.git.LFSLikeAccess(path, nonCasGitHead)
	require.NoError(t, err)
}

func TestGit_IndexFromTree_Clean(t *testing.T) {
	stagePath := env.NonCasTestNextPath()
	require.NoError(t, MkdirTest(stagePath))

	files := map[string]string{
		"file1.txt": "hello world\n",
		"file2.txt": "test content\n",
	}

	head, treeDir, err := env.git.CreateStageRepo(stagePath, env.mountObjDir, files)
	require.NoError(t, err)

	casPath := env.GitTestNextPath()
	require.NoError(t, MkdirTest(casPath))

	// Populate identical files on CAS filesystem
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(casPath, name), []byte(content), 0644))
	}

	status, err := env.git.TreeIndexTest(casPath, treeDir, head)
	require.NoError(t, err)
	require.Empty(t, status, "Expected clean git status when workspace matches tree")
}

// Deleted File: file2.txt is in TreeDirectory but absent on disk
func TestGit_IndexFromTree_DeletedFile(t *testing.T) {
	stagePath := env.NonCasTestNextPath()
	require.NoError(t, MkdirTest(stagePath))

	files := map[string]string{
		"file1.txt": "existing file\n",
		"file2.txt": "deleted file\n",
	}

	head, treeDir, err := env.git.CreateStageRepo(stagePath, env.mountObjDir, files)
	require.NoError(t, err)

	casPath := env.GitTestNextPath()
	require.NoError(t, MkdirTest(casPath))

	// Only write file1.txt to disk; file2.txt is absent
	require.NoError(t, os.WriteFile(filepath.Join(casPath, "file1.txt"), []byte(files["file1.txt"]), 0644))

	status, err := env.git.TreeIndexTest(casPath, treeDir, head)
	require.NoError(t, err)
	require.Contains(t, status, " D file2.txt", "Expected deleted file to be detected in git status")
}

// Modified File: file1.txt modified locally
func TestGit_IndexFromTree_ModifiedFile(t *testing.T) {
	stagePath := env.NonCasTestNextPath()
	require.NoError(t, MkdirTest(stagePath))

	files := map[string]string{
		"file1.txt": "original content\n",
	}

	head, treeDir, err := env.git.CreateStageRepo(stagePath, env.mountObjDir, files)
	require.NoError(t, err)

	casPath := env.GitTestNextPath()
	require.NoError(t, MkdirTest(casPath))

	// Write modified content on disk
	require.NoError(t, os.WriteFile(filepath.Join(casPath, "file1.txt"), []byte("modified content\n"), 0644))

	status, err := env.git.TreeIndexTest(casPath, treeDir, head)
	require.NoError(t, err)
	require.Contains(t, status, " M file1.txt", "Expected modified file to be detected in git status")
}

// Untracked File: new untracked file added to worktree
func TestGit_IndexFromTree_UntrackedFile(t *testing.T) {
	stagePath := env.NonCasTestNextPath()
	require.NoError(t, MkdirTest(stagePath))

	files := map[string]string{
		"tracked.txt": "tracked content\n",
	}

	head, treeDir, err := env.git.CreateStageRepo(stagePath, env.mountObjDir, files)
	require.NoError(t, err)

	casPath := env.GitTestNextPath()
	require.NoError(t, MkdirTest(casPath))

	require.NoError(t, os.WriteFile(filepath.Join(casPath, "tracked.txt"), []byte(files["tracked.txt"]), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(casPath, "untracked.txt"), []byte("new untracked\n"), 0644))

	status, err := env.git.TreeIndexTest(casPath, treeDir, head)
	require.NoError(t, err)
	require.Contains(t, status, "?? untracked.txt", "Expected untracked file in git status")
}

// Mode Change: chmod +x on script.sh
func TestGit_IndexFromTree_ModeChange(t *testing.T) {
	stagePath := env.NonCasTestNextPath()
	require.NoError(t, MkdirTest(stagePath))

	files := map[string]string{
		"script.sh": "#!/bin/sh\necho hi\n",
	}

	head, treeDir, err := env.git.CreateStageRepo(stagePath, env.mountObjDir, files)
	require.NoError(t, err)

	casPath := env.GitTestNextPath()
	require.NoError(t, MkdirTest(casPath))

	// Write as executable (0755) on disk
	require.NoError(t, os.WriteFile(filepath.Join(casPath, "script.sh"), []byte(files["script.sh"]), 0755))

	status, err := env.git.TreeIndexTest(casPath, treeDir, head)
	require.NoError(t, err)
	require.Contains(t, status, " M script.sh", "Expected mode change in git status")
}

// Directory in the tree replaced by a regular file on disk.
func TestGit_IndexFromTree_DirReplacedByFile(t *testing.T) {
	stagePath := env.NonCasTestNextPath()
	require.NoError(t, MkdirTest(stagePath))

	files := map[string]string{
		"sub/deeper/nested.txt": "nested content\n",
	}
	head, treeDir, err := env.git.CreateStageRepo(stagePath, env.mountObjDir, files)
	require.NoError(t, err)

	casPath := env.GitTestNextPath()
	require.NoError(t, MkdirTest(casPath))

	// "sub" exists as a regular file, not a directory
	require.NoError(t, os.WriteFile(filepath.Join(casPath, "sub"), []byte("now a file\n"), 0644))

	status, err := env.git.TreeIndexTest(casPath, treeDir, head)
	require.NoError(t, err, "must not fail with ENOTDIR")
	require.Contains(t, status, " D sub/deeper/nested.txt")
	require.Contains(t, status, "?? sub")
}

// Regular file in the tree replaced by a symlink on disk.
func TestGit_IndexFromTree_FileReplacedBySymlink(t *testing.T) {
	stagePath := env.NonCasTestNextPath()
	require.NoError(t, MkdirTest(stagePath))

	files := map[string]string{
		"target.txt": "target\n",
		"file.txt":   "regular content\n",
	}

	head, treeDir, err := env.git.CreateStageRepo(stagePath, env.mountObjDir, files)
	require.NoError(t, err)

	casPath := env.GitTestNextPath()
	require.NoError(t, MkdirTest(casPath))

	require.NoError(t, os.WriteFile(filepath.Join(casPath, "target.txt"), []byte(files["target.txt"]), 0644))
	require.NoError(t, os.Symlink("target.txt", filepath.Join(casPath, "file.txt")))

	status, err := env.git.TreeIndexTest(casPath, treeDir, head)
	require.NoError(t, err)
	require.Contains(t, status, " T file.txt", "type change should be reported")
}

// Directory in the tree replaced by a symlink to another directory. Verifies
// the scan completes and reports the tree directory's contents as deleted,
// with the symlink itself untracked.
func TestGit_IndexFromTree_DirReplacedBySymlinkToDir(t *testing.T) {
	stagePath := env.NonCasTestNextPath()
	require.NoError(t, MkdirTest(stagePath))

	files := map[string]string{
		"sub/tracked.txt":   "sub content\n",
		"other/tracked.txt": "other content\n",
	}

	head, treeDir, err := env.git.CreateStageRepo(stagePath, env.mountObjDir, files)
	require.NoError(t, err)

	casPath := env.GitTestNextPath()
	require.NoError(t, MkdirTest(casPath))

	// "other" is a real directory holding a file whose basename collides with
	// the one in the tree under "sub".
	otherDir := filepath.Join(casPath, "other")
	require.NoError(t, os.MkdirAll(otherDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "tracked.txt"),
		[]byte(files["other/tracked.txt"]), 0644))

	// "sub" is a symlink to "other" rather than a directory.
	require.NoError(t, os.Symlink("other", filepath.Join(casPath, "sub")))

	status, err := env.git.TreeIndexTest(casPath, treeDir, head)
	require.NoError(t, err)

	// The tree directory is gone, so its file is deleted rather than picking up
	// stat data from the same-named file in the symlink target.
	require.Contains(t, status, " D sub/tracked.txt")
	require.Contains(t, status, "?? sub")
}
func TestGit_IndexFromTree_SymlinkedDirDeepStatAliasing(t *testing.T) {
	stagePath := env.NonCasTestNextPath()
	require.NoError(t, MkdirTest(stagePath))

	files := map[string]string{
		"dir1/inner_dir/file1": "content one\n",
		"dir2/inner_dir/file1": "content two\n",
	}

	head, treeDir, err := env.git.CreateStageRepo(stagePath, env.mountObjDir, files)
	require.NoError(t, err)

	casPath := env.GitTestNextPath()
	require.NoError(t, MkdirTest(casPath))

	// dir2 is materialized for real.
	require.NoError(t, os.MkdirAll(filepath.Join(casPath, "dir2", "inner_dir"), 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(casPath, "dir2", "inner_dir", "file1"),
		[]byte(files["dir2/inner_dir/file1"]), 0644))

	// dir1 is a symlink to dir2, so dir1/inner_dir/file1 resolves to a real
	// file that is NOT the one the tree describes.
	require.NoError(t, os.Symlink("dir2", filepath.Join(casPath, "dir1")))

	status, err := env.git.TreeIndexTest(casPath, treeDir, head)
	require.NoError(t, err)

	require.Contains(t, status, " D dir1/inner_dir/file1",
		"tree path must be reported deleted, not aliased to the symlink target")
	require.Contains(t, status, "?? dir1")
	require.NotContains(t, status, "dir2/inner_dir/file1",
		"the real file must stay clean")

	// The aliasing is invisible to status once the entry is correctly deleted,
	// so assert on the index directly.
	dump, err := gitindex.Dump(filepath.Join(casPath, ".git", "index"))
	require.NoError(t, err)

	byPath := map[string]gitindex.IndexEntry{}
	for _, e := range dump.Entries {
		byPath[e.Path] = e
	}

	ghost, ok := byPath["dir1/inner_dir/file1"]
	require.True(t, ok, "tree entry must still be present so git reports it deleted")
	require.Zero(t, ghost.Ino, "must not carry stat data from the symlink target")
	require.Zero(t, ghost.Dev)
	require.Zero(t, ghost.MtimeSec)

	real := byPath["dir2/inner_dir/file1"]
	require.NotEqual(t, real.Ino, ghost.Ino, "stat data must not alias across paths")
}
