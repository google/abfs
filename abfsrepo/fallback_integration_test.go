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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/abfs/abfsrepo"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

type stubCASStore struct {
	entities.CASStore
}

func (s *stubCASStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	return nil, os.ErrNotExist
}

func (s *stubCASStore) SearchRefs(server, project, ref string, limit int) ([]entities.ProjectRefHash, error) {
	return nil, nil
}

func (s *stubCASStore) Close() error {
	return nil
}

type stubCASFS struct {
	bindcas.CASFS
}

func (s *stubCASFS) SetExcluded(path string, val bool) error {
	return nil
}

func (s *stubCASFS) CreateGitObjects(path, directory string) error {
	return nil
}

func (s *stubCASFS) HashStat(path string) (*entities.HashStat, error) {
	return nil, os.ErrNotExist
}

func (s *stubCASFS) Hash(path string) (entities.Hash, error) {
	return entities.Hash{}, os.ErrNotExist
}

func (s *stubCASFS) Mknod(path string, mode entities.FileMode, rDev int) error {
	return nil
}

func (s *stubCASFS) SetHash(path string, h entities.Hash) error {
	return nil
}

func (s *stubCASFS) SetHashSize(path string, h entities.Hash, size int64) error {
	return nil
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s failed: %v\nOutput: %s", strings.Join(args, " "), dir, err, string(out))
	}
	return string(out)
}

func initGitRepo(t *testing.T, dir string, args ...string) {
	t.Helper()
	// Try with --ref-format=files first in case the host Git defaults to reftable (Git 2.45+)
	tryArgs := append([]string{"init", "--ref-format=files"}, args...)
	cmd := exec.Command("git", tryArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if err := cmd.Run(); err != nil {
		// Fallback to standard git init without --ref-format for older Git versions (< 2.45)
		runGit(t, dir, append([]string{"init"}, args...)...)
	}
}

func setupRemoteRepo(t *testing.T, bareDir string, branch string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(bareDir, 0755); err != nil {
		t.Fatalf("failed to create bare dir: %v", err)
	}
	initGitRepo(t, bareDir, "--bare", "-b", branch)

	// Create a temp worktree to commit the files and push to bareDir
	tmpWork := t.TempDir()
	initGitRepo(t, tmpWork, "-b", branch)
	runGit(t, tmpWork, "remote", "add", "origin", bareDir)

	for p, content := range files {
		fullPath := filepath.Join(tmpWork, p)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("failed to create dir for file %s: %v", p, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write file %s: %v", p, err)
		}
	}

	runGit(t, tmpWork, "add", "-A")
	runGit(t, tmpWork, "commit", "-m", "Initial commit")
	runGit(t, tmpWork, "push", "origin", branch)
}

func TestFallbackToGit_EndToEnd(t *testing.T) {
	testDir := t.TempDir()

	// 1. Setup simulated remote repos
	remotesDir := filepath.Join(testDir, "remotes")
	manifestRemote := filepath.Join(remotesDir, "manifest.git")
	buildRemote := filepath.Join(remotesDir, "platform", "build")
	kernelRemote := filepath.Join(remotesDir, "kernel", "common")
	gitRepoRemote := filepath.Join(remotesDir, "git-repo.git")

	manifestXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<manifest>
  <remote name="origin" fetch="%s" review="review.example.com" />
  <default revision="main" remote="origin" />
  <project path="build/make" name="platform/build">
    <copyfile src="Android.bp" dest="Android.bp" />
  </project>
  <project path="kernel/common" name="kernel/common" revision="android14-6.1" />
</manifest>
`, remotesDir)

	setupRemoteRepo(t, manifestRemote, "main", map[string]string{
		"default.xml": manifestXML,
	})

	setupRemoteRepo(t, buildRemote, "main", map[string]string{
		"Android.bp":  "// Build configuration\n",
		"version.txt": "1.0.0\n",
	})

	setupRemoteRepo(t, kernelRemote, "android14-6.1", map[string]string{
		"Makefile":     "# Kernel makefile\n",
		"arch/x86/def": "CONFIG_X86=y\n",
	})

	setupRemoteRepo(t, gitRepoRemote, "stable", map[string]string{
		"repo": "#!/bin/sh\n",
	})

	// 2. Initialize workspace with abfsrepo
	wsDir := filepath.Join(testDir, "workspace")
	if err := os.MkdirAll(wsDir, 0755); err != nil {
		t.Fatalf("failed to create workspace dir: %v", err)
	}

	ctx := context.Background()
	store := &stubCASStore{}
	casfs := &stubCASFS{}
	opts := abfsrepo.RepoOpts{
		Store: store,
		CASFS: casfs,
		Git:   casgit.NewWithCASFS(store, casfs),
	}
	repo := abfsrepo.NewRepoForceRoot(opts, wsDir)

	t.Log("==> Testing abfsrepo Init with FallbackToGit")
	initOpts := abfsrepo.InitOpts{
		ManifestURL:    manifestRemote,
		ManifestBranch: "main",
		ManifestName:   "default.xml",
		RepoURL:        gitRepoRemote,
		RepoRev:        "stable",
		FallbackToGit:  true,
	}

	if err := repo.Init(ctx, initOpts); err != nil {
		t.Fatalf("repo.Init failed: %v", err)
	}

	// Verify manifest setup
	manifestDefault := filepath.Join(wsDir, ".repo", "manifests", "default.xml")
	if _, err := os.Stat(manifestDefault); err != nil {
		t.Fatalf("expected manifest file %s to exist: %v", manifestDefault, err)
	}
	masterManifest := filepath.Join(wsDir, ".repo", "manifest.xml")
	manifestContent, err := os.ReadFile(masterManifest)
	if err != nil {
		t.Fatalf("failed to read %s: %v", masterManifest, err)
	}
	if !strings.Contains(string(manifestContent), `include name="default.xml"`) {
		t.Errorf("expected master manifest to include default.xml, got: %s", string(manifestContent))
	}

	// Verify .repo/manifests/.git is a relative symlink to ../manifests.git
	manifestDotGit := filepath.Join(wsDir, ".repo", "manifests", ".git")
	target, err := os.Readlink(manifestDotGit)
	if err != nil {
		t.Fatalf("failed to readlink %s: %v", manifestDotGit, err)
	}
	if target != "../manifests.git" {
		t.Errorf("manifest .git symlink target = %s, expected ../manifests.git", target)
	}

	t.Log("==> Testing abfsrepo Sync with FallbackToGit")
	syncOpts := abfsrepo.SyncOpts{
		Jobs:          2,
		FallbackToGit: true,
	}
	if err := repo.Sync(ctx, nil, syncOpts); err != nil {
		t.Fatalf("repo.Sync failed: %v", err)
	}

	// 3. Verify checked out files
	buildBP := filepath.Join(wsDir, "build", "make", "Android.bp")
	data, err := os.ReadFile(buildBP)
	if err != nil {
		t.Fatalf("failed to read %s: %v", buildBP, err)
	}
	if string(data) != "// Build configuration\n" {
		t.Errorf("unexpected content in %s: %q", buildBP, string(data))
	}

	buildVersion := filepath.Join(wsDir, "build", "make", "version.txt")
	if _, err := os.Stat(buildVersion); err != nil {
		t.Fatalf("expected %s to exist: %v", buildVersion, err)
	}

	// Verify copyfile
	rootBP := filepath.Join(wsDir, "Android.bp")
	copyData, err := os.ReadFile(rootBP)
	if err != nil {
		t.Fatalf("expected copyfile %s to exist: %v", rootBP, err)
	}
	if string(copyData) != "// Build configuration\n" {
		t.Errorf("unexpected copyfile content: %q", string(copyData))
	}

	// Verify kernel project
	kernelMakefile := filepath.Join(wsDir, "kernel", "common", "Makefile")
	if _, err := os.Stat(kernelMakefile); err != nil {
		t.Fatalf("expected %s to exist: %v", kernelMakefile, err)
	}

	// 4. Verify .git relative symlinks and git directory layout
	buildMakeDir := filepath.Join(wsDir, "build", "make")
	dotGitPath := filepath.Join(buildMakeDir, ".git")
	link, err := os.Readlink(dotGitPath)
	if err != nil {
		t.Fatalf("failed to readlink %s: %v", dotGitPath, err)
	}
	expectedRel := filepath.Join("..", "..", ".repo", "projects", "build", "make.git")
	if filepath.Clean(link) != filepath.Clean(expectedRel) {
		t.Errorf("dotGit symlink = %s, expected %s", link, expectedRel)
	}

	// 5. Test developer git commands inside the checkout
	t.Log("==> Testing git status in checked out worktree")
	statusOut := runGit(t, buildMakeDir, "status", "--porcelain")
	if strings.TrimSpace(statusOut) != "" {
		t.Errorf("expected clean git status, got: %s", statusOut)
	}

	t.Log("==> Testing developer branch creation and commit inside worktree")
	runGit(t, buildMakeDir, "checkout", "-b", "feature-branch")
	newFilePath := filepath.Join(buildMakeDir, "new_feature.go")
	if err := os.WriteFile(newFilePath, []byte("package feature\n"), 0644); err != nil {
		t.Fatalf("failed to write new file: %v", err)
	}
	runGit(t, buildMakeDir, "add", "new_feature.go")
	runGit(t, buildMakeDir, "commit", "-m", "Add new feature")

	logOut := runGit(t, buildMakeDir, "log", "-n", "1", "--oneline")
	if !strings.Contains(logOut, "Add new feature") {
		t.Errorf("expected git log to contain 'Add new feature', got: %s", logOut)
	}

	// 6. Test re-sync idempotency
	t.Log("==> Testing re-sync idempotency")
	if err := repo.Sync(ctx, nil, syncOpts); err != nil {
		t.Fatalf("re-sync failed: %v", err)
	}

	// Re-verify symlink is intact
	newLink, err := os.Readlink(dotGitPath)
	if err != nil {
		t.Fatalf("failed to readlink %s after re-sync: %v", dotGitPath, err)
	}
	if filepath.Clean(newLink) != filepath.Clean(expectedRel) {
		t.Errorf("after re-sync dotGit symlink = %s, expected %s", newLink, expectedRel)
	}

	// Verify developer branch and commit are preserved
	branchLogOut := runGit(t, buildMakeDir, "log", "-n", "1", "--oneline", "feature-branch")
	if !strings.Contains(branchLogOut, "Add new feature") {
		t.Errorf("expected feature-branch to retain 'Add new feature', got: %s", branchLogOut)
	}
}
