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
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/manifest"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

// plainDirsFS creates git objects directories as plain directories.
type plainDirsFS struct {
	bindcas.CASFS
}

func (f *plainDirsFS) CreateGitObjects(path, directory string) error {
	return os.MkdirAll(filepath.Join(path, directory), 0755)
}

func (f *plainDirsFS) SetExcluded(path string, val bool) error {
	return nil
}

// branchlessStore has no refs, so a clone stops when it resolves its branch.
type branchlessStore struct {
	entities.CASStore
}

func (s *branchlessStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	return make([]entities.Hash, len(refs)), nil
}

func (s *branchlessStore) SearchRefs(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return nil, nil
}

// newNestedRepo loads a manifest with an outer project at a and an inner one at a/child, and
// creates the given files and directories (a trailing slash) in the workspace.
func newNestedRepo(t *testing.T, files ...string) *Repo {
	t.Helper()
	root := t.TempDir()
	manifests := filepath.Join(root, ".repo", "manifests")
	require.NoError(t, os.MkdirAll(manifests, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".repo", "manifest.xml"), []byte(`<manifest>
  <include name="default.xml" />
</manifest>
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(manifests, "default.xml"), []byte(`<manifest>
  <remote name="origin" fetch="https://gerrit.example.com" />
  <default revision="main" remote="origin" />
  <project name="platform/outer" path="a" />
  <project name="platform/inner" path="a/child" />
  <project name="platform/deep" path="a/x/y" />
</manifest>
`), 0644))
	for _, f := range files {
		p := filepath.Join(root, f)
		if f[len(f)-1] == '/' {
			require.NoError(t, os.MkdirAll(p, 0755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0755))
		require.NoError(t, os.WriteFile(p, []byte(f), 0644))
	}

	casfs := &plainDirsFS{}
	store := &branchlessStore{}
	r := NewRepoForceRoot(RepoOpts{Store: store, CASFS: casfs, Git: casgit.NewWithCASFS(store, casfs)}, root)
	require.NoError(t, r.loadManifest(context.Background()))
	r.groupsInclude, r.groupsExclude = FilterGroups(nil)
	r.filterProjectsByGroups()
	return r
}

// requireFiles checks which of the given workspace paths exist.
func requireFiles(t *testing.T, r *Repo, exist, gone []string) {
	t.Helper()
	for _, f := range exist {
		_, err := os.Lstat(filepath.Join(r.repoRoot, f))
		require.NoError(t, err)
	}
	for _, f := range gone {
		_, err := os.Lstat(filepath.Join(r.repoRoot, f))
		require.ErrorIs(t, err, os.ErrNotExist, f)
	}
}

// A project inside another project's work tree is cloned by casgit, without the git fallback.
// The store has no refs, so a clone that passes its checks stops at the branch.
func TestSyncProject_NestedClone(t *testing.T) {
	tests := []struct {
		name  string
		clone func(r *Repo) error
	}{
		{
			name: "clone",
			clone: func(r *Repo) error {
				return r.syncProject(context.Background(), r.projectsByPath["a/child"], SyncOpts{})
			},
		},
		{
			name: "optimized git fallback",
			clone: func(r *Repo) error {
				project := r.projectsByPath["a/child"]
				u, err := url.Parse("https://gerrit.example.com/platform/inner")
				require.NoError(t, err)
				return r.tryOptimizedFallback(context.Background(), project, filepath.Join(r.repoRoot, "a/child"), r.projectDotGitPath(project),
					filepath.Join(r.projectObjectsPath(project), "objects"), "origin", "main", "main", u)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newNestedRepo(t, "a/.git/", "a/version.txt")
			err := tt.clone(r)
			require.ErrorContains(t, err, "remote branch main not found")
			require.NotContains(t, err.Error(), "cannot clone inside")
		})
	}
}

// A clone into a directory that already holds other files keeps them, as repo does: an outer
// project cloned around an inner one, or an inner project below files of the outer one.
func TestSyncProject_IntoDirectoryWithFiles(t *testing.T) {
	tests := []struct {
		name    string
		project string
		files   []string
	}{
		{name: "outer around inner", project: "a", files: []string{"a/child/.git/", "a/child/local-work.txt"}},
		{name: "inner below outer files", project: "a/child", files: []string{"a/.git/", "a/child/README"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newNestedRepo(t, tt.files...)
			err := r.syncProject(context.Background(), r.projectsByPath[tt.project], SyncOpts{})
			require.ErrorContains(t, err, "remote branch main not found")
			requireFiles(t, r, tt.files, nil)
		})
	}
}

// A file where a project's work tree or a directory above it should be fails the project and
// names the project that has the file, as repo fails to create the work tree.
func TestSyncProject_FileAtProjectPath(t *testing.T) {
	tests := []struct {
		name    string
		project string
		files   []string
		wantErr string
	}{
		{name: "at the path", project: "a/child", files: []string{"a/.git/", "a/child"}, wantErr: "file a/child lies in the work tree of project platform/outer"},
		{name: "above the path", project: "a/x/y", files: []string{"a/.git/", "a/x"}, wantErr: "file a/x lies in the work tree of project platform/outer"},
		{name: "no project around", project: "a", files: []string{"a"}, wantErr: "a is not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newNestedRepo(t, tt.files...)
			err := r.syncProject(context.Background(), r.projectsByPath[tt.project], SyncOpts{FallbackToGit: true})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// When the outer project's tree has a file where an inner project's work tree is, at the path or
// below it, the error names both projects.
func TestRepositoryInWorkTree(t *testing.T) {
	tests := []struct {
		name string
		path string
		repo string
		want string
	}{
		{name: "at the path", path: "a/child", repo: "a/child", want: "cannot check out project platform/outer: its file a/child would replace the work tree of project platform/inner"},
		{name: "below the path", path: "a/x", repo: "a/x/y", want: "cannot check out project platform/outer: its file a/x would replace the work tree of project platform/deep"},
		{name: "not a project", path: "a/z", repo: "a/z/w", want: "cannot check out project platform/outer: its file a/z would replace the work tree of the repository at a/z/w"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newNestedRepo(t)
			err := r.repositoryInWorkTree(r.projectsByPath["a"], &casgit.RepositoryInWorkTreeError{
				Path:       filepath.Join(r.repoRoot, tt.path),
				Repository: filepath.Join(r.repoRoot, tt.repo),
			})
			require.EqualError(t, err, tt.want)
		})
	}
}

// Removing a project keeps the directories of projects inside it, and their changes, as repo
// deletes an obsolete checkout.
func TestSyncProjectRemoves_SparesNestedProjects(t *testing.T) {
	tests := []struct {
		name   string
		remove []string
		exist  []string
		gone   []string
	}{
		{
			name:   "outer",
			remove: []string{"a"},
			exist:  []string{"a/child/.git", "a/child/version.txt", "a/child/local-work.txt", "a/x/y/.git"},
			gone:   []string{"a/.git", "a/version.txt", "a/sub", "a/x/y.txt"},
		},
		{
			name:   "inner",
			remove: []string{"a/child"},
			exist:  []string{"a/.git", "a/version.txt", "a/sub/file.txt", "a/x/y/.git"},
			gone:   []string{"a/child"},
		},
		{
			name:   "outer and inner",
			remove: []string{"a", "a/child", "a/x/y"},
			gone:   []string{"a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newNestedRepo(t, "a/.git/", "a/version.txt", "a/sub/file.txt", "a/x/y.txt",
				"a/child/.git/", "a/child/version.txt", "a/child/local-work.txt", "a/x/y/.git/")
			r.curProjects = map[string]struct{}{"a": {}, "a/child": {}, "a/x/y": {}}
			for _, p := range tt.remove {
				delete(r.projectsByPath, p)
			}
			require.NoError(t, r.syncProjectRemoves())
			requireFiles(t, r, tt.exist, tt.gone)
			for _, p := range tt.remove {
				require.NotContains(t, r.curProjects, p)
			}
		})
	}
}

// A project whose work tree is gone after its checkout is reported.
func TestCheckWorkTree(t *testing.T) {
	tests := []struct {
		path    string
		wantErr string
	}{
		{path: "a"},
		{path: "a/child", wantErr: "project platform/inner: work tree a/child is missing after sync"},
		{path: "a/x/y", wantErr: "project platform/deep: work tree a/x/y is missing after sync"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			r := newNestedRepo(t, "a/.git/", "a/child/version.txt")
			err := r.checkWorkTree(r.projectsByPath[tt.path])
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

// After the checkouts, link file failures and missing work trees fail their projects, each
// project once, and a missing work tree leaves the project list.
func TestFinishCheckouts(t *testing.T) {
	tests := []struct {
		name       string
		links      []string
		files      []string
		wantFailed []string
		wantErrs   int
		wantListed bool
	}{
		// A link onto a directory that holds files fails.
		{name: "link file fails", links: []string{"a"}, files: []string{"a/child/.git/"}, wantFailed: []string{"platform/outer"}, wantErrs: 1, wantListed: true},
		{name: "work tree missing", wantFailed: []string{"platform/inner"}, wantErrs: 1},
		{name: "both", links: []string{"a/child"}, wantFailed: []string{"platform/inner"}, wantErrs: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newNestedRepo(t, append([]string{"a/.git/", "a/sub/file.txt", "a/child/version.txt", "a/x/y/.git/"}, tt.files...)...)
			r.curProjects = map[string]struct{}{}
			var synced []*manifest.Project
			for _, p := range []string{"a/x/y", "a/child", "a"} {
				synced = append(synced, r.projectsByPath[p])
				r.curProjects[p] = struct{}{}
			}
			for _, p := range tt.links {
				r.projectsByPath[p].Linkfile = []*manifest.Linkfile{{Src: "version.txt", Dest: "a/sub"}}
			}

			errs, failed := r.finishCheckouts(synced)
			require.Equal(t, tt.wantFailed, failed)
			require.Len(t, errs, tt.wantErrs)
			_, listed := r.curProjects["a/child"]
			require.Equal(t, tt.wantListed, listed)
		})
	}
}
