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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/abfsrepo"
	"github.com/google/abfs/casgit"
)

// pushBranch commits files on top of branch base of bareDir and pushes them as branch.
func pushBranch(t *testing.T, bareDir, base, branch string, files map[string]string) {
	t.Helper()
	work := t.TempDir()
	runGit(t, work, "clone", "-q", "-b", base, bareDir, ".")
	if branch != base {
		runGit(t, work, "checkout", "-q", "-b", branch)
	}
	for p, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(work, p), []byte(content), 0644))
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-q", "-m", "Update "+branch)
	runGit(t, work, "push", "-q", "origin", branch)
}

type syncTest struct {
	ctx        context.Context
	remotesDir string
	wsDir      string
	repo       *abfsrepo.Repo
}

// newSyncTest sets up a workspace whose manifest lists platform/shared, a project with a main
// and a release branch, at the given paths.
func newSyncTest(t *testing.T, projects string) *syncTest {
	t.Helper()
	testDir := t.TempDir()
	s := &syncTest{
		ctx:        context.Background(),
		remotesDir: filepath.Join(testDir, "remotes"),
		wsDir:      filepath.Join(testDir, "workspace"),
	}
	manifestRemote := filepath.Join(s.remotesDir, "manifest.git")
	sharedRemote := filepath.Join(s.remotesDir, "platform", "shared")
	gitRepoRemote := filepath.Join(s.remotesDir, "git-repo.git")

	setupRemoteRepo(t, manifestRemote, "main", map[string]string{"default.xml": s.manifest(projects)})
	setupRemoteRepo(t, sharedRemote, "main", map[string]string{"version.txt": "main\n"})
	pushBranch(t, sharedRemote, "main", "release", map[string]string{"version.txt": "release\n"})
	setupRemoteRepo(t, gitRepoRemote, "stable", map[string]string{"repo": "#!/bin/sh\n"})

	require.NoError(t, os.MkdirAll(s.wsDir, 0755))
	store := &stubCASStore{}
	casfs := &stubCASFS{}
	s.repo = abfsrepo.NewRepoForceRoot(abfsrepo.RepoOpts{
		Store: store,
		CASFS: casfs,
		Git:   casgit.NewWithCASFS(store, casfs),
	}, s.wsDir)
	require.NoError(t, s.repo.Init(s.ctx, abfsrepo.InitOpts{
		ManifestURL:    manifestRemote,
		ManifestBranch: "main",
		ManifestName:   "default.xml",
		RepoURL:        gitRepoRemote,
		RepoRev:        "stable",
		FallbackToGit:  true,
	}))
	return s
}

func (s *syncTest) manifest(projects string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<manifest>
  <remote name="origin" fetch="%s" />
  <default revision="main" remote="origin" />
%s
</manifest>
`, s.remotesDir, projects)
}

func (s *syncTest) sync(t *testing.T) {
	t.Helper()
	require.NoError(t, s.repo.Sync(s.ctx, nil, abfsrepo.SyncOpts{Jobs: 2, FallbackToGit: true}))
}

func (s *syncTest) version(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.wsDir, path, "version.txt"))
	require.NoError(t, err)
	return strings.TrimSpace(string(b))
}

func (s *syncTest) projectsList(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.wsDir, ".repo", abfsrepo.ProjectsListFile))
	require.NoError(t, err)
	return strings.Fields(string(b))
}

func TestSync_OneNameAtTwoPaths(t *testing.T) {
	s := newSyncTest(t, `  <project name="platform/shared" path="a/shared" />
  <project name="platform/shared" path="b/shared" revision="release" />`)

	s.sync(t)
	require.Equal(t, "main", s.version(t, "a/shared"))
	require.Equal(t, "release", s.version(t, "b/shared"))
	require.Equal(t, []string{"a/shared", "b/shared"}, s.projectsList(t))
}

func TestSync_OldWorkspaceGainsMissingPaths(t *testing.T) {
	projects := `  <project name="platform/shared" path="a/shared" />
  <project name="platform/shared" path="b/shared" revision="release" />`
	s := newSyncTest(t, `  <project name="platform/shared" path="b/shared" revision="release" />`)
	s.sync(t)
	require.Equal(t, []string{"b/shared"}, s.projectsList(t))

	// The shared objects directory of the name stays in place.
	objects := filepath.Join(s.wsDir, ".repo", "project-objects", "platform", "shared.git")
	require.NoError(t, os.MkdirAll(objects, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(objects, "marker"), nil, 0644))

	// The manifest checkout is edited in place: the git fallback of the manifest update keeps it.
	require.NoError(t, os.WriteFile(filepath.Join(s.wsDir, ".repo", "manifests", "default.xml"), []byte(s.manifest(projects)), 0644))
	s.sync(t)
	require.Equal(t, "main", s.version(t, "a/shared"))
	require.Equal(t, "release", s.version(t, "b/shared"))
	require.Equal(t, []string{"a/shared", "b/shared"}, s.projectsList(t))
	require.FileExists(t, filepath.Join(objects, "marker"))
}

func TestSync_ByNameSyncsEveryPath(t *testing.T) {
	s := newSyncTest(t, `  <project name="platform/shared" path="a/shared" />
  <project name="platform/shared" path="b/shared" revision="release" />`)

	require.NoError(t, s.repo.Sync(s.ctx, []string{"platform/shared"}, abfsrepo.SyncOpts{Jobs: 2, FallbackToGit: true}))
	require.Equal(t, "main", s.version(t, "a/shared"))
	require.Equal(t, "release", s.version(t, "b/shared"))
}
