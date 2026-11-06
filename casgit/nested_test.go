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

package casgit

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/cas/fs/gitindex"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

// dirFS creates git objects directories as plain directories.
type dirFS struct {
	bindcas.CASFS
}

func (f *dirFS) SetExcluded(path string, val bool) error {
	return nil
}

func (f *dirFS) CreateGitObjects(path, directory string) error {
	return os.MkdirAll(filepath.Join(path, directory), 0755)
}

// emptyRefsStore has no refs, so a clone stops when it resolves its branch.
type emptyRefsStore struct {
	entities.CASStore
}

func (s *emptyRefsStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	return make([]entities.Hash, len(refs)), nil
}

func TestClone_Nested(t *testing.T) {
	tests := []struct {
		name    string
		dir     string
		nested  bool
		wantErr string
	}{
		{name: "inside a repository", dir: "parent/child", wantErr: "cannot clone inside an existing git repository"},
		// The clone passes the checks and stops at the branch, which the store does not have.
		{name: "nested inside a repository", dir: "parent/child", nested: true, wantErr: "remote branch main not found"},
		{name: "not empty", dir: "other", wantErr: "is not empty"},
		{name: "nested and not empty", dir: "other", nested: true, wantErr: "remote branch main not found"},
		{name: "nested with a .git element", dir: "parent/.git/child", nested: true, wantErr: "has a .git element"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, d := range []string{"parent/.git", "other"} {
				require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(root, d, "file.txt"), nil, 0644))
			}
			before := readTree(t, root)

			g := NewWithCASFS(&emptyRefsStore{}, &dirFS{})
			err := g.Clone(context.Background(), CloneOptions{
				RemoteRepo: "https://gerrit.example.com/platform/child",
				Directory:  filepath.Join(root, tt.dir),
				GitDir:     filepath.Join(root, ".repo", "projects", "child.git"),
				ObjectsDir: filepath.Join(root, ".repo", "project-objects", "child.git", "objects"),
				Origin:     "origin",
				Branch:     "main",
				Nested:     tt.nested,
				Quiet:      true,
			})
			require.ErrorContains(t, err, tt.wantErr)
			// The work trees around the clone are left as they were.
			require.Equal(t, before, readTree(t, root))
		})
	}
}

// readTree lists the files and directories below root, except .repo.
func readTree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == ".repo" {
			return filepath.SkipDir
		}
		paths = append(paths, rel)
		return nil
	}))
	return paths
}

// fakeTree is a git tree of files and subtrees, by name.
type fakeTree struct {
	name  string
	sha   entities.Hash
	files []string
	dirs  []*fakeTree
}

func (f *fakeTree) Name() string  { return f.name }
func (f *fakeTree) Sha() [20]byte { return f.sha }

func (f *fakeTree) Files(ctx context.Context) ([]gitindex.FileEntry, error) {
	var files []gitindex.FileEntry
	for _, name := range f.files {
		files = append(files, gitindex.FileEntry{Name: name})
	}
	return files, nil
}

func (f *fakeTree) Subtrees(ctx context.Context) ([]gitindex.TreeDirectory, error) {
	var dirs []gitindex.TreeDirectory
	for _, d := range f.dirs {
		dirs = append(dirs, d)
	}
	return dirs, nil
}

// A clone into a directory that holds files of another project replaces what is in the way of
// its own tree and keeps the rest, as git read-tree --reset -u does for repo.
func TestClearTreePaths(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"README", "version.txt", "src/a.c", "src/b.c", "lib", "doc/x.md"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("outer"), 0644))
	}
	tree := &fakeTree{
		files: []string{"version.txt", "doc"},
		dirs: []*fakeTree{
			{name: "src", files: []string{"a.c"}},
			{name: "lib", files: []string{"l.c"}},
		},
	}

	require.NoError(t, clearTreePaths(context.Background(), dir, tree))
	require.Equal(t, []string{".", "README", "src", "src/b.c"}, readTree(t, dir))
}

// A directory that holds another repository, at the directory or below it, is kept with its
// files, as repo keeps it when it removes a project. When the tree has a file there, the clear
// fails and names the directory and the repository.
func TestClearTreePaths_KeepsRepositories(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		tree     *fakeTree
		wantPath string
		wantRepo string
		// want is the work tree after the clear; nil means unchanged.
		want []string
	}{
		{
			name:  "the tree has a directory",
			files: []string{"child/.git/HEAD", "child/README", "child/work.txt"},
			tree:  &fakeTree{dirs: []*fakeTree{{name: "child", files: []string{"README"}}}},
		},
		{
			name:     "repository at the path",
			files:    []string{"apps/.git/HEAD", "apps/local-work.txt"},
			tree:     &fakeTree{files: []string{"apps"}},
			wantPath: "apps",
			wantRepo: "apps",
		},
		{
			name:     "repository one level below",
			files:    []string{"apps/a/.git/HEAD", "apps/a/work.txt"},
			tree:     &fakeTree{files: []string{"apps"}},
			wantPath: "apps",
			wantRepo: "apps/a",
		},
		{
			name:     "repository three levels below",
			files:    []string{"apps/top.txt", "apps/a/b/c/.git/HEAD", "apps/a/b/c/work.txt"},
			tree:     &fakeTree{files: []string{"apps"}},
			wantPath: "apps",
			wantRepo: "apps/a/b/c",
		},
		{
			name:     "repository below a subtree",
			files:    []string{"x/y/z/.git/HEAD", "x/y/z/work.txt"},
			tree:     &fakeTree{dirs: []*fakeTree{{name: "x", files: []string{"y"}}}},
			wantPath: "x/y",
			wantRepo: "x/y/z",
		},
		{
			name:  "no repository below",
			files: []string{"apps/a/b/file.txt", "other/file.txt"},
			tree:  &fakeTree{files: []string{"apps"}},
			want:  []string{".", "other", "other/file.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("inner"), 0644))
			}
			want := tt.want
			if want == nil {
				want = readTree(t, dir)
			}

			err := clearTreePaths(context.Background(), dir, tt.tree)
			if tt.wantPath == "" {
				require.NoError(t, err)
			} else {
				var inWorkTree *RepositoryInWorkTreeError
				require.ErrorAs(t, err, &inWorkTree)
				require.Equal(t, filepath.Join(dir, tt.wantPath), inWorkTree.Path)
				require.Equal(t, filepath.Join(dir, tt.wantRepo), inWorkTree.Repository)
			}
			require.Equal(t, want, readTree(t, dir))
		})
	}
}

// excludedFS records the last exclusion set for each path.
type excludedFS struct {
	dirFS
	excluded map[string]bool
}

func (f *excludedFS) SetExcluded(path string, val bool) error {
	f.excluded[path] = val
	return nil
}

// A failed nested clone into a directory that already holds files puts the directory back into
// the hash of the work tree around it, since the files stay.
func TestClone_NestedFailureIncludesDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "parent", "child")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("outer"), 0644))
	casfs := &excludedFS{excluded: map[string]bool{}}

	g := NewWithCASFS(&emptyRefsStore{}, casfs)
	err := g.Clone(context.Background(), CloneOptions{
		RemoteRepo: "https://gerrit.example.com/platform/child",
		Directory:  dir,
		GitDir:     filepath.Join(root, ".repo", "projects", "parent", "child.git"),
		ObjectsDir: filepath.Join(root, ".repo", "project-objects", "child.git", "objects"),
		Origin:     "origin",
		Branch:     "main",
		Nested:     true,
		Quiet:      true,
	})
	require.ErrorContains(t, err, "remote branch main not found")
	excluded, ok := casfs.excluded[dir]
	require.True(t, ok)
	require.False(t, excluded)
	require.FileExists(t, filepath.Join(dir, "README"))
}

// deepTreeFS records the work tree at the time its deeptree is set.
type deepTreeFS struct {
	dirFS
	setDeepTree func(path string)
}

func (f *deepTreeFS) SetDeepTree(path string, h entities.Hash) error {
	f.setDeepTree(path)
	return nil
}

// A nested clone clears what is in the way of its tree before it sets the deeptree, since casfs
// keeps an existing entry on a name clash. Other clones leave the work tree alone.
func TestGitClone_SetWorkTree(t *testing.T) {
	tests := []struct {
		name   string
		nested bool
		files  []string
		want   []string
	}{
		{name: "nested", nested: true, files: []string{"version.txt"}, want: []string{"."}},
		{name: "not nested", files: []string{"README", "version.txt"}, want: []string{".", "README", "version.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("outer"), 0644))
			}
			var got []string
			casfs := &deepTreeFS{setDeepTree: func(path string) {
				require.Equal(t, dir, path)
				got = readTree(t, dir)
			}}
			c := &GitClone{GitRepo: &GitRepo{casgit: NewWithCASFS(&emptyRefsStore{}, casfs)}, CloneOptions: CloneOptions{Directory: dir, Nested: tt.nested}}

			require.NoError(t, c.setWorkTree(context.Background(), &fakeTree{files: []string{"version.txt"}}, entities.Hash{}))
			require.Equal(t, tt.want, got)
		})
	}
}

// treeStore serves trees by hash.
type treeStore struct {
	emptyRefsStore
	trees map[entities.Hash][]entities.FileEntry
}

func (s *treeStore) GetTree(ctx context.Context, key entities.Hash) ([]byte, entities.TreeMeta, error) {
	entries, ok := s.trees[key]
	if !ok {
		return nil, entities.TreeMeta{}, entities.NotExistErrf("no tree %s", key)
	}
	var body bytes.Buffer
	var meta entities.TreeMeta
	for _, e := range entries {
		fmt.Fprintf(&body, "%s %s\x00", e.Mode.String(), e.Name)
		body.Write(e.Hash[:])
		meta.Sizes = append(meta.Sizes, e.Size)
	}
	return append(fmt.Appendf(nil, "tree %d\x00", body.Len()), body.Bytes()...), meta, nil
}

// nodeFS creates files as plain files and records the hashes, sizes and deeptrees set.
type nodeFS struct {
	dirFS
	lock      sync.Mutex
	hashes    map[string]entities.Hash
	sizes     map[string]int64
	deepTrees []string
}

func (f *nodeFS) Mknod(path string, mode entities.FileMode, rDev int) error {
	return os.WriteFile(path, nil, 0644)
}

func (f *nodeFS) SetHashSize(path string, h entities.Hash, s int64) error {
	f.lock.Lock()
	f.sizes[path] = s
	f.lock.Unlock()
	return f.SetHash(path, h)
}

func (f *nodeFS) SetHash(path string, h entities.Hash) error {
	f.lock.Lock()
	defer f.lock.Unlock()
	f.hashes[path] = h
	return nil
}

func (f *nodeFS) SetDeepTree(path string, h entities.Hash) error {
	f.deepTrees = append(f.deepTrees, path)
	return nil
}

// A nested clone into a directory that is not empty, as an outer project checked out around an
// inner one, adds the entries of its tree that are missing and keeps the others. It sets no
// deeptree there: casfs would set the hash of each existing directory the deeptree reaches to the
// tree's, so the inner work tree's own files would drop out of its hash and of the workspace
// snapshot.
func TestGitClone_SetWorkTreeAroundRepository(t *testing.T) {
	h := func(c byte) entities.Hash {
		var x entities.Hash
		for i := range x {
			x[i] = c
		}
		return x
	}
	root, child, src, lib := h(1), h(2), h(3), h(4)
	version, readme, childVersion, ac := h(5), h(6), h(7), h(8)
	store := &treeStore{trees: map[entities.Hash][]entities.FileEntry{
		root: {
			{Name: "child", Mode: entities.FileModeDir, Hash: child},
			{Name: "lib", Mode: entities.FileModeDir, Hash: lib},
			{Name: "src", Mode: entities.FileModeDir, Hash: src},
			{Name: "version.txt", Mode: entities.FileModeRegular, Hash: version, Size: 6},
		},
		child: {
			{Name: "README", Mode: entities.FileModeRegular, Hash: readme, Size: 12},
			{Name: "version.txt", Mode: entities.FileModeRegular, Hash: childVersion, Size: 14},
		},
		src: {{Name: "a.c", Mode: entities.FileModeRegular, Hash: ac, Size: 3}},
	}}
	tree := &fakeTree{
		sha:   root,
		files: []string{"version.txt"},
		dirs: []*fakeTree{
			{name: "child", sha: child, files: []string{"README", "version.txt"}},
			{name: "lib", sha: lib, files: []string{"l.c"}},
			{name: "src", sha: src, files: []string{"a.c"}},
		},
	}

	dir := t.TempDir()
	for f, content := range map[string]string{
		"child/.git/HEAD":   "ref: refs/heads/main",
		"child/version.txt": "inner",
		"child/work.txt":    "work",
		"src/a.c":           "old",
		"src/local.c":       "local",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte(content), 0644))
	}
	casfs := &nodeFS{hashes: map[string]entities.Hash{}, sizes: map[string]int64{}}
	bindcas.SetGlobalCASFS(casfs)
	t.Cleanup(func() { bindcas.SetGlobalCASFS(bindcas.NewExternalCASFS("")) })
	c := &GitClone{GitRepo: &GitRepo{casgit: NewWithCASFS(store, casfs)}, CloneOptions: CloneOptions{Directory: dir, Nested: true}}

	require.NoError(t, c.setWorkTree(context.Background(), tree, h(9)))
	require.Empty(t, casfs.deepTrees)
	require.Equal(t, []string{".", "child", "child/.git", "child/.git/HEAD", "child/README", "child/version.txt", "child/work.txt", "lib", "src", "src/a.c", "src/local.c", "version.txt"}, readTree(t, dir))
	for f, content := range map[string]string{"child/version.txt": "inner", "child/work.txt": "work", "src/local.c": "local"} {
		got, err := os.ReadFile(filepath.Join(dir, f))
		require.NoError(t, err)
		require.Equal(t, content, string(got))
	}
	want := map[string]entities.Hash{"version.txt": version, "child/README": readme, "src/a.c": ac, "lib": lib}
	got := map[string]entities.Hash{}
	for p, hash := range casfs.hashes {
		rel, err := filepath.Rel(dir, p)
		require.NoError(t, err)
		got[rel] = hash
	}
	require.Equal(t, want, got)
	wantSizes := map[string]int64{"version.txt": 6, "child/README": 12, "src/a.c": 3}
	gotSizes := map[string]int64{}
	for p, size := range casfs.sizes {
		rel, err := filepath.Rel(dir, p)
		require.NoError(t, err)
		gotSizes[rel] = size
	}
	require.Equal(t, wantSizes, gotSizes)
}
