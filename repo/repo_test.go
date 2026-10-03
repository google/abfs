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

package repo

import (
	"context"
	"io"
	"path"
	"runtime"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/cas/cas"

	"github.com/google/abfs/shared/entities"
)

func NewTestRepo(manifestServer, manifestProjectName, manifestScheme string) *Repo {
	return New(&testStore{}, &testCASGit{}, manifestServer, manifestProjectName, manifestScheme, "", false)
}

func NewTestLocalManifestRepo(localDir string) *Repo {
	return New(&testStore{}, &testCASGit{}, "", "", "", path.Join(testDataDir(), localDir), false)
}

func testDataDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("couldn't get runtime caller")
	}
	return path.Join(path.Dir(thisFile), "testdata")
}

func TestDefaultSchemeURL(t *testing.T) {
	require := require.New(t)

	r := NewTestRepo("somehost.googlesource.com/child1/child2", "manifest", "https")
	require.NoError(r.ManifestFromFile(path.Join(testDataDir(), "different_remote.xml")))
	require.Equal("https://somehost.googlesource.com/child1/aosp", r.defaultSchemeURL)
}

func TestIncludeLocalManifestDir(t *testing.T) {
	require := require.New(t)

	r := NewTestLocalManifestRepo("local")
	require.NoError(r.ManifestFromFile(path.Join(testDataDir(), "different_remote.xml")))
	man := r.PinnedManifest()
	localInclude := false
	for _, p := range man.Project {
		if p.Path == "local_path" {
			localInclude = true
			break
		}
	}
	require.Equal(localInclude, true)
}

func TestRemoteRevision(t *testing.T) {
	require := require.New(t)

	manifestXML := `
<manifest>
  <remote  name="aosp"
           fetch="https://android.googlesource.com/"
           revision="main-kernel-2025"
           review="https://android-review.googlesource.com/" />
  <remote  name="partner"
           fetch=".."
           review="https://review.example.com/" />
  <default revision="android16-6.12"
           remote="partner"
           sync-c="true"
           sync-j="32" />
  <project name="platform/external/some-project" path="external/some-project" remote="aosp" />
  <project name="platform/vendor/some-vendor-project" path="vendor/some-vendor-project" />
  <project name="platform/external/main-project" path="external/main-project" revision="main" />
</manifest>
`

	store := &testStore{}
	r := New(store, &testCASGit{}, "somehost.googlesource.com/child1/child2", "manifest", "https", "", false)

	mainKernelHash := entities.NewHash("1234567890abcdef1234567890abcdef12345678")
	android16Hash := entities.NewHash("fedcba0987654321fedcba0987654321fedcba09")
	mainHash := entities.NewHash("0123456789012345678901234567890123456789")

	store.searchRefsFunc = func(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
		if refPrefix == "main-kernel-2025" || refPrefix == "refs/heads/main-kernel-2025" {
			return []entities.ProjectRefHash{
				{
					Project: "platform/external/some-project",
					Ref:     refPrefix,
					Hash:    mainKernelHash,
				},
			}, nil
		}
		if refPrefix == "android16-6.12" || refPrefix == "refs/heads/android16-6.12" {
			return []entities.ProjectRefHash{
				{
					Project: "platform/vendor/some-vendor-project",
					Ref:     refPrefix,
					Hash:    android16Hash,
				},
			}, nil
		}
		if refPrefix == "main" || refPrefix == "refs/heads/main" {
			return []entities.ProjectRefHash{
				{
					Project: "platform/external/main-project",
					Ref:     refPrefix,
					Hash:    mainHash,
				},
			}, nil
		}
		return nil, nil
	}

	require.NoError(r.ManifestFromBytes([]byte(manifestXML)))

	man := r.PinnedManifest()
	require.NotNil(man)
	require.Len(man.Project, 3)

	require.Equal("platform/external/some-project", man.Project[0].Name)
	require.Equal(mainKernelHash.String(), man.Project[0].Revision)

	require.Equal("platform/vendor/some-vendor-project", man.Project[1].Name)
	require.Equal(android16Hash.String(), man.Project[1].Revision)

	require.Equal("platform/external/main-project", man.Project[2].Name)
	require.Equal(mainHash.String(), man.Project[2].Revision)
}

type testStore struct {
	searchRefsFunc func(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error)
}

func (t *testStore) CAS() cas.Store {
	return nil
}

// AddMapResult implements entities.CASStore.
func (t *testStore) AddMapResult(ctx context.Context, from entities.Hash, to entities.Hash) error {
	panic("unimplemented")
}

// BlobPath implements entities.CASStore.
func (t *testStore) BlobPath(ctx context.Context, key entities.Hash) string {
	panic("unimplemented")
}

// Close implements entities.CASStore.
func (t *testStore) Close() error {
	panic("unimplemented")
}

// GetObject implements entities.CASStore.
func (t *testStore) GetObject(ctx context.Context, key entities.Hash) ([]byte, error) {
	panic("unimplemented")
}

// GetRefs implements entities.CASStore.
func (t *testStore) GetRefs(server string, project string, refs []string) ([]entities.Hash, error) {
	panic("unimplemented")
}

// GetTree implements entities.CASStore.
func (t *testStore) GetTree(ctx context.Context, key entities.Hash) ([]byte, entities.TreeMeta, error) {
	panic("unimplemented")
}

// MapResults implements entities.CASStore.
func (t *testStore) MapResults(ctx context.Context, id entities.Hash) ([]entities.Hash, error) {
	panic("unimplemented")
}

// ObjectPath implements entities.CASStore.
func (t *testStore) ObjectPath(ctx context.Context, key entities.Hash) string {
	panic("unimplemented")
}

// OpenBlob implements entities.CASStore.
func (t *testStore) OpenBlob(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	panic("unimplemented")
}

// OpenObjectFile implements entities.CASStore.
func (t *testStore) OpenObjectFile(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	panic("unimplemented")
}

// PutObject implements entities.CASStore.
func (t *testStore) PutObject(ctx context.Context, key entities.Hash, content []byte) error {
	panic("unimplemented")
}

// PutRefs implements entities.CASStore.
func (t *testStore) PutRefs(server string, project string, refs []entities.RefHash) error {
	panic("unimplemented")
}

// PutTree implements entities.CASStore.
func (t *testStore) PutTree(ctx context.Context, key entities.Hash, content []byte, meta entities.TreeMeta) error {
	panic("unimplemented")
}

// RefreshMapResults implements entities.CASStore.
func (t *testStore) RefreshMapResults(ctx context.Context, id entities.Hash) error {
	panic("unimplemented")
}

// SearchRefs implements entities.CASStore.
func (t *testStore) SearchRefs(server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	if t.searchRefsFunc != nil {
		return t.searchRefsFunc(server, project, refPrefix, limit)
	}
	panic("unimplemented")
}

// WriteBlob implements entities.CASStore.
func (t *testStore) WriteBlob(ctx context.Context, key entities.Hash, size int64, reader io.Reader) error {
	panic("unimplemented")
}

// WriteObject implements entities.CASStore.
func (t *testStore) WriteObject(ctx context.Context, key entities.Hash, reader io.Reader) error {
	panic("unimplemented")
}

// ObjectHeader implements entities.CASStore.
func (t *testStore) ObjectHeader(ctx context.Context, key entities.Hash) (entities.ObjectHeader, error) {
	panic("unimplemented")
}

// ObjectHeader implements entities.CASStore.
func (t *testStore) Stat(ctx context.Context, key entities.Hash) (entities.StatInfo, error) {
	panic("unimplemented")
}

// ObjectType implements entities.CASStore.
func (t *testStore) ObjectType(ctx context.Context, key entities.Hash) (entities.ObjectType, error) {
	panic("unimplemented")
}

// ObjectType implements entities.CASStore.
func (t *testStore) KeepAlive(ctx context.Context, key entities.Hash) error {
	panic("unimplemented")
}

// ObjectType implements entities.CASStore.
func (t *testStore) IsObjectRemote(ctx context.Context, key entities.Hash) (bool, error) {
	panic("unimplemented")
}

func (t *testStore) StartGC(ctx context.Context) error {
	panic("unimplemented")
}

func (t *testStore) HealthCheck() error {
	panic("unimplemented")
}

type testCASGit struct {
}

// GetCommit implements CASGit.
func (t *testCASGit) GetCommit(ctx context.Context, key entities.Hash) (*object.Commit, error) {
	panic("unimplemented")
}

// ResolveCommit implements CASGit.
func (t *testCASGit) ResolveCommit(ctx context.Context, key entities.Hash) (*object.Commit, error) {
	panic("unimplemented")
}

func TestNonHTTPSRemoteResolution(t *testing.T) {
	require := require.New(t)

	manifestXML := `
<manifest>
  <remote  name="ssh-remote"
           fetch="ssh://git.example.com/" />
  <default revision="main"
           remote="ssh-remote" />
  <project name="platform/external/some-project" path="external/some-project" />
</manifest>
`

	store := &testStore{}
	r := New(store, &testCASGit{}, "somehost.googlesource.com/child1/child2", "manifest", "https", "", false)

	mainHash := entities.NewHash("1234567890abcdef1234567890abcdef12345678")

	store.searchRefsFunc = func(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
		// CRITICAL ASSERTION: The repo package should pass the ssh:// URL directly
		// to the store, and let the store client handle the scheme stripping.
		require.Equal("ssh://git.example.com/", server)

		if refPrefix == "main" || refPrefix == "refs/heads/main" {
			return []entities.ProjectRefHash{
				{
					Project: "platform/external/some-project",
					Ref:     refPrefix,
					Hash:    mainHash,
				},
			}, nil
		}
		return nil, nil
	}

	require.NoError(r.ManifestFromBytes([]byte(manifestXML)))

	man := r.PinnedManifest()
	require.NotNil(man)
	require.Len(man.Project, 1)
	require.Equal(mainHash.String(), man.Project[0].Revision)
}
