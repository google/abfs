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
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

// objectsFS creates git objects directories as casfs does: an existing name fails with EEXIST.
type objectsFS struct {
	bindcas.CASFS
}

func (f *objectsFS) CreateGitObjects(path, directory string) error {
	dir := filepath.Join(path, directory)
	if _, err := os.Lstat(dir); err == nil {
		return &os.PathError{Op: "mkdir", Path: dir, Err: syscall.EEXIST}
	}
	return os.MkdirAll(dir, 0755)
}

func (f *objectsFS) SetExcluded(path string, val bool) error {
	return nil
}

// noRefsStore has no refs, so a clone stops when it resolves its revision.
type noRefsStore struct {
	entities.CASStore
}

func (s *noRefsStore) GetRefs(server, project string, refs []string) ([]entities.Hash, error) {
	return make([]entities.Hash, len(refs)), nil
}

func (s *noRefsStore) SearchRefs(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return nil, nil
}

// The second path of a name clones into the objects directory the first path created, without
// the git fallback.
func TestSyncProject_SecondPathReusesObjectsDir(t *testing.T) {
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
  <project name="platform/shared" path="a/shared" />
  <project name="platform/shared" path="b/shared" revision="release" />
</manifest>
`), 0644))
	casfs := &objectsFS{}
	store := &noRefsStore{}
	r := NewRepoForceRoot(RepoOpts{Store: store, CASFS: casfs, Git: casgit.NewWithCASFS(store, casfs)}, root)
	ctx := context.Background()
	require.NoError(t, r.loadManifest(ctx))
	r.groupsInclude, r.groupsExclude = FilterGroups(nil)
	r.filterProjectsByGroups()

	// The clone of a/shared created the shared objects directory.
	project := r.projectsByPath["b/shared"]
	objects := filepath.Join(r.projectObjectsPath(project), "objects")
	require.NoError(t, casfs.CreateGitObjects(filepath.Dir(objects), filepath.Base(objects)))
	require.NoError(t, os.WriteFile(filepath.Join(objects, "marker"), nil, 0644))

	// The store has no refs, so the clone stops at the revision, after the objects directory.
	err := r.syncProject(ctx, project, SyncOpts{})
	require.Error(t, err)
	require.NotErrorIs(t, err, fs.ErrExist)
	require.ErrorContains(t, err, "release")
	require.FileExists(t, filepath.Join(objects, "marker"))
}
