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
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/abfsrepo"
	"github.com/google/abfs/casgit"
)

type nestedWorkspace struct {
	remotes string
	ws      string
	repo    *abfsrepo.Repo
	casfs   *excludedCASFS
}

// excludedCASFS records the last exclusion set for each path.
type excludedCASFS struct {
	stubCASFS
	mu       sync.Mutex
	excluded map[string]bool
}

func (s *excludedCASFS) SetExcluded(path string, val bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.excluded[path] = val
	return nil
}

func (s *excludedCASFS) isExcluded(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.excluded[path]
}

func nestedManifest(remotes, projects string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<manifest>
  <remote name="origin" fetch="%s" />
  <default revision="main" remote="origin" />
%s
</manifest>
`, remotes, projects)
}

// initNested inits a workspace whose manifest lists the given projects. repos holds the files of
// each project's remote.
func initNested(t *testing.T, repos map[string]map[string]string, projects string) *nestedWorkspace {
	t.Helper()
	testDir := t.TempDir()
	remotes := filepath.Join(testDir, "remotes")
	manifestRemote := filepath.Join(remotes, "manifest.git")
	gitRepoRemote := filepath.Join(remotes, "git-repo.git")
	setupRemoteRepo(t, manifestRemote, "main", map[string]string{"default.xml": nestedManifest(remotes, projects)})
	for name, files := range repos {
		setupRemoteRepo(t, filepath.Join(remotes, name), "main", files)
	}
	setupRemoteRepo(t, gitRepoRemote, "stable", map[string]string{"repo": "#!/bin/sh\n"})

	wsDir := filepath.Join(testDir, "workspace")
	require.NoError(t, os.MkdirAll(wsDir, 0755))
	store := &stubCASStore{}
	casfs := &excludedCASFS{excluded: map[string]bool{}}
	repo := abfsrepo.NewRepoForceRoot(abfsrepo.RepoOpts{
		Store: store,
		CASFS: casfs,
		Git:   casgit.NewWithCASFS(store, casfs),
	}, wsDir)
	require.NoError(t, repo.Init(context.Background(), abfsrepo.InitOpts{
		ManifestURL:    manifestRemote,
		ManifestBranch: "main",
		ManifestName:   "default.xml",
		RepoURL:        gitRepoRemote,
		RepoRev:        "stable",
		FallbackToGit:  true,
	}))
	return &nestedWorkspace{remotes: remotes, ws: wsDir, repo: repo, casfs: casfs}
}

func (w *nestedWorkspace) sync() error {
	return w.repo.Sync(context.Background(), nil, abfsrepo.SyncOpts{Jobs: 2, FallbackToGit: true})
}

// setManifest changes the manifest in the workspace, as a local edit, for the next sync.
func (w *nestedWorkspace) setManifest(t *testing.T, projects string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(w.ws, ".repo", "manifests", "default.xml"), []byte(nestedManifest(w.remotes, projects)), 0644))
}

// versionRepos gives every name a remote with version.txt holding the name.
func versionRepos(names ...string) map[string]map[string]string {
	repos := make(map[string]map[string]string, len(names))
	for _, name := range names {
		repos[name] = map[string]string{"version.txt": name + "\n"}
	}
	return repos
}

func requireFile(t *testing.T, wsDir, path, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(wsDir, path))
	require.NoError(t, err)
	require.Equal(t, want, string(b))
}

func requireVersion(t *testing.T, wsDir, path, want string) {
	t.Helper()
	requireFile(t, wsDir, filepath.Join(path, "version.txt"), want+"\n")
}

// A project inside another project's path is checked out inside the other project's work tree,
// whatever the order of the two in the manifest.
func TestSync_NestedProjects(t *testing.T) {
	parent := `  <project name="platform/parent" path="a" />`
	child := `  <project name="platform/child" path="a/child" />`
	grandchild := `  <project name="platform/grandchild" path="a/child/deep" />`
	sibling := `  <project name="platform/sibling" path="a-b" />`
	repos := versionRepos("platform/parent", "platform/child", "platform/grandchild", "platform/sibling")

	tests := []struct {
		name     string
		projects string
	}{
		{name: "parent first", projects: parent + "\n" + child + "\n" + grandchild + "\n" + sibling},
		{name: "child first", projects: grandchild + "\n" + child + "\n" + sibling + "\n" + parent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := initNested(t, repos, tt.projects)
			require.NoError(t, w.sync())
			requireVersion(t, w.ws, "a", "platform/parent")
			requireVersion(t, w.ws, "a/child", "platform/child")
			requireVersion(t, w.ws, "a/child/deep", "platform/grandchild")
			requireVersion(t, w.ws, "a-b", "platform/sibling")
		})
	}
}

// A link or copy file whose destination lies inside another project's path does not keep that
// project from being checked out.
func TestSync_FilesInsideAnotherProject(t *testing.T) {
	links := `  <project name="platform/links" path="l">
    <linkfile src="version.txt" dest="b/sub/linked.txt" />
    <copyfile src="version.txt" dest="b/copied.txt" />
  </project>`
	target := `  <project name="platform/target" path="b/sub" />`
	other := `  <project name="platform/other" path="b" />`
	repos := versionRepos("platform/links", "platform/target", "platform/other")

	tests := []struct {
		name     string
		projects string
	}{
		{name: "links first", projects: links + "\n" + target + "\n" + other},
		{name: "links last", projects: other + "\n" + target + "\n" + links},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := initNested(t, repos, tt.projects)
			require.NoError(t, w.sync())
			requireVersion(t, w.ws, "b", "platform/other")
			requireVersion(t, w.ws, "b/sub", "platform/target")
			requireFile(t, w.ws, "b/sub/linked.txt", "platform/links\n")
			requireFile(t, w.ws, "b/copied.txt", "platform/links\n")
		})
	}
}

// A second sync that removes the outer project, or adds one around an existing inner project,
// keeps the inner project and its uncommitted work.
func TestSync_ResyncNestedProjects(t *testing.T) {
	parent := `  <project name="platform/parent" path="a" />`
	child := `  <project name="platform/child" path="a/child" />`
	repos := versionRepos("platform/parent", "platform/child")

	tests := []struct {
		name         string
		first, again string
		parentAfter  bool
	}{
		{name: "outer removed", first: parent + "\n" + child, again: child},
		{name: "outer added", first: child, again: parent + "\n" + child, parentAfter: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := initNested(t, repos, tt.first)
			require.NoError(t, w.sync())
			require.NoError(t, os.WriteFile(filepath.Join(w.ws, "a/child/local-work.txt"), []byte("work\n"), 0644))

			w.setManifest(t, tt.again)
			require.NoError(t, w.sync())
			requireVersion(t, w.ws, "a/child", "platform/child")
			requireFile(t, w.ws, "a/child/local-work.txt", "work\n")
			if tt.parentAfter {
				requireVersion(t, w.ws, "a", "platform/parent")
			} else {
				require.NoFileExists(t, filepath.Join(w.ws, "a/version.txt"))
				require.NoFileExists(t, filepath.Join(w.ws, "a/.git"))
			}
		})
	}
}

// When the outer project has files at the inner project's path, the inner checkout keeps them
// and its own files win, as repo's checkout does.
func TestSync_OuterFilesAtInnerPath(t *testing.T) {
	repos := map[string]map[string]string{
		"platform/parent": {"version.txt": "platform/parent\n", "child/README": "parent readme\n", "child/version.txt": "parent version\n"},
		"platform/child":  {"version.txt": "platform/child\n"},
	}
	w := initNested(t, repos, `  <project name="platform/parent" path="a" />
  <project name="platform/child" path="a/child" />`)
	// A second sync updates both projects in place and gives the same result.
	for range 2 {
		require.NoError(t, w.sync())
		requireVersion(t, w.ws, "a/child", "platform/child")
		requireFile(t, w.ws, "a/child/README", "parent readme\n")
		require.Equal(t, " M child/version.txt\n", runGit(t, filepath.Join(w.ws, "a"), "status", "--porcelain", "-uno"))
	}
}

// A failed inner checkout leaves the outer project's files at the inner path in place, and in the
// outer work tree's hash.
func TestSync_FailedInnerKeepsOuterFiles(t *testing.T) {
	repos := map[string]map[string]string{
		"platform/parent": {"version.txt": "platform/parent\n", "child/README": "parent readme\n"},
	}
	w := initNested(t, repos, `  <project name="platform/parent" path="a" />
  <project name="platform/child" path="a/child" />`)
	require.Error(t, w.sync())
	requireFile(t, w.ws, "a/child/README", "parent readme\n")
	require.NoFileExists(t, filepath.Join(w.ws, "a/child/.git"))
	require.Empty(t, runGit(t, filepath.Join(w.ws, "a"), "status", "--porcelain", "-uno"))
	require.False(t, w.casfs.isExcluded(filepath.Join(w.ws, "a/child")))
}
