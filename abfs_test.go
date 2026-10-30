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

package abfs

import (
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"maps"
	"net"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/manifest"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/grpc/client"
	"github.com/google/abfs/shared/remote/grpc/server"
	"github.com/google/abfs/shared/remote/servers/filebased"
)

func TestSplitNestedProjects(t *testing.T) {
	paths := []string{
		"a/b/c",
		"a/b/d",
		"a/b/c/a",
		"a/b/c/a/b",
		"x/y/z",
		"x",
	}
	projects := make([]*manifest.Project, len(paths))
	for i, p := range paths {
		q := p
		projects[i] = &manifest.Project{Path: q}
	}
	todo, nested, _ := splitNestedProjects(projects)

	if assert.Len(t, todo, 3) {
		assert.Equal(t, "a/b/c", todo[0].Path)
		assert.Equal(t, "a/b/d", todo[1].Path)
		assert.Equal(t, "x", todo[2].Path)
	}
	if assert.Len(t, nested, 3) {
		assert.Equal(t, "a/b/c/a", nested[0].Path)
		assert.Equal(t, "a/b/c/a/b", nested[1].Path)
		assert.Equal(t, "x/y/z", nested[2].Path)
	}

	fullPath := "/im/a/full/path"
	projects = append(projects, &manifest.Project{Path: fullPath})
	_, _, err := splitNestedProjects(projects)
	assert.False(t, err == nil)
}

func TestShouldExcludeProject(t *testing.T) {
	projectA := manifest.Project{Groups: []string{"a", "b"}}
	projectB := manifest.Project{Groups: []string{"c", "d", "notdefault"}}
	assert.False(t, shouldExcludeProject(&projectA, []string{}))              // group filter is not specified
	assert.False(t, shouldExcludeProject(&projectA, []string{"a", "c", "d"})) // group filter matches
	assert.False(t, shouldExcludeProject(&projectA, []string{"all"}))         // 'all' should include all projects
	assert.False(t, shouldExcludeProject(&projectA, []string{"default"}))     // default should include non notdefault project
	assert.True(t, shouldExcludeProject(&projectA, []string{"-a", "c", "d"})) // group filter matches with -
	assert.False(t, shouldExcludeProject(&projectA, []string{"-a", "b"}))     // match in order
	assert.True(t, shouldExcludeProject(&projectA, []string{"c", "d"}))       // group filter does not match
	assert.False(t, shouldExcludeProject(&projectB, []string{"c", "d"}))      // group filter matches
	assert.False(t, shouldExcludeProject(&projectB, []string{"all"}))         // 'all' should include project with notdefault specified
	assert.True(t, shouldExcludeProject(&projectB, []string{"default"}))      // notdefault project should be excluded from default
}

type mockGitCloner struct {
	cloneCalled bool
	lastOpts    casgit.CloneOptions
}

func (m *mockGitCloner) Clone(ctx context.Context, opts casgit.CloneOptions) error {
	m.cloneCalled = true
	m.lastOpts = opts
	return nil
}

type mockShellGit struct {
	fetchCalled    bool
	checkoutCalled bool
	lastRef        string
}

func (m *mockShellGit) FetchRef(repoPath string, remote string, ref string) error {
	m.fetchCalled = true
	m.lastRef = ref
	return nil
}

func (m *mockShellGit) Checkout(repoPath string, ref string) error {
	m.checkoutCalled = true
	return nil
}

func TestTryOptimizedGitFallback(t *testing.T) {
	mockGit := &mockGitCloner{}
	mockShGit := &mockShellGit{}

	// Case 1: Revision is a hash, should fallback to HEAD
	project := &manifest.Project{
		Name:     "test-project",
		Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}
	opts := casgit.CloneOptions{
		Directory: "/tmp/test",
		Origin:    "origin",
	}

	err := tryOptimizedGitFallbackImpl(mockGit, mockShGit, context.Background(), project, opts, nil, nil, "main")
	assert.NoError(t, err)
	assert.True(t, mockGit.cloneCalled)
	assert.Equal(t, "main", mockGit.lastOpts.Branch)
	assert.True(t, mockGit.lastOpts.SingleBranch)
	assert.True(t, mockGit.lastOpts.NoTags)
	assert.True(t, mockShGit.fetchCalled)
	assert.True(t, mockShGit.checkoutCalled)
	assert.Equal(t, project.Revision, mockShGit.lastRef)

	// Case 2: Upstream is provided, should fallback to Upstream (ignoring fallbackBranch)
	mockGit.cloneCalled = false
	mockShGit.fetchCalled = false
	project.Upstream = "dev"
	err = tryOptimizedGitFallbackImpl(mockGit, mockShGit, context.Background(), project, opts, nil, nil, "main")
	assert.NoError(t, err)
	assert.Equal(t, "dev", mockGit.lastOpts.Branch)

	// Case 3: No upstream and no fallbackBranch, should fallback to empty string (default remote HEAD symref)
	mockGit.cloneCalled = false
	mockShGit.fetchCalled = false
	project.Upstream = ""
	err = tryOptimizedGitFallbackImpl(mockGit, mockShGit, context.Background(), project, opts, nil, nil, "")
	assert.NoError(t, err)
	assert.True(t, mockGit.cloneCalled)
	assert.Equal(t, "", mockGit.lastOpts.Branch)

	// Case 4: fallbackBranch is "HEAD", should sanitize to empty string
	mockGit.cloneCalled = false
	mockShGit.fetchCalled = false
	err = tryOptimizedGitFallbackImpl(mockGit, mockShGit, context.Background(), project, opts, nil, nil, "HEAD")
	assert.NoError(t, err)
	assert.True(t, mockGit.cloneCalled)
	assert.Equal(t, "", mockGit.lastOpts.Branch)
}

const (
	testManifestServer  = "gerrit.example.com"
	testManifestProject = "platform/manifest"
)

func putTestObject(t *testing.T, s remote.RemoteProvider, objType string, content []byte) entities.Hash {
	var buf bytes.Buffer
	require.NoError(t, entities.WriteObject(objType, content, &buf))
	key := entities.Hash(sha1.Sum(buf.Bytes()))
	require.NoError(t, s.PutObject(context.Background(), key, remote.NewReaderFromBytes(buf.Bytes())))
	return key
}

// putTestManifestCommit stores a manifest commit whose default.xml names one remote.
func putTestManifestCommit(t *testing.T, s remote.RemoteProvider, remoteName string) entities.Hash {
	xml := fmt.Sprintf(`<manifest><remote name=%q fetch=".." /><default revision="main" remote=%q /></manifest>`, remoteName, remoteName)
	blob := putTestObject(t, s, "blob", []byte(xml))
	var tree bytes.Buffer
	tree.WriteString("100644 default.xml\x00")
	tree.Write(blob[:])
	treeHash := putTestObject(t, s, "tree", tree.Bytes())
	return putTestObject(t, s, "commit", fmt.Appendf(nil, "tree %s\nauthor A <a@example.com> 1 +0000\ncommitter A <a@example.com> 1 +0000\n\n%s\n", treeHash, remoteName))
}

// newTestManifestServer starts a file-based server over gRPC with one manifest commit per ref.
// The map goes from ref to the remote name in that commit's manifest; the result goes from
// remote name to commit.
func newTestManifestServer(t *testing.T, refs map[string]string) (*client.ABFSClient, map[string]entities.Hash) {
	require := require.New(t)
	fserver, err := filebased.New(t.TempDir())
	require.NoError(err)
	s, err := server.NewGRPCServer(&server.GRPCServerOpts{Server: fserver})
	require.NoError(err)
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(err)
	go func() { _ = s.Serve(lis) }()
	c, err := client.NewABFSClient(&client.ABFSClientOpts{Servers: []string{lis.Addr().String()}, AuthInfo: instance.CreateAuthInfo(instance.AuthTypeNone, "", "", true)})
	require.NoError(err)
	t.Cleanup(func() {
		_ = c.Close()
		s.Stop()
		_ = fserver.Close()
	})

	commits := map[string]entities.Hash{}
	for _, ref := range slices.Sorted(maps.Keys(refs)) {
		h := putTestManifestCommit(t, c, refs[ref])
		commits[refs[ref]] = h
		require.NoError(c.PutRefs(context.Background(), testManifestServer, testManifestProject, []entities.RefHash{{Ref: ref, Hash: h}}))
	}
	return c, commits
}

func putTestHead(t *testing.T, c *client.ABFSClient, target string) {
	head := &gitobjects.SymRefObject{Content: target}
	require.NoError(t, head.Encode())
	require.NoError(t, c.PutObject(context.Background(), head.Hash, remote.NewReaderFromBytes(head.Encoded)))
	require.NoError(t, c.PutRefs(context.Background(), testManifestServer, testManifestProject, []entities.RefHash{{Ref: "HEAD", Hash: head.Hash}}))
}

// mountTestStore serves the store until stopReads is set. After that it records and fails every
// object read, so createManifestProject stops at its first commit read, which names the commit
// it took.
type mountTestStore struct {
	entities.CASStore
	mu        sync.Mutex
	stopReads bool
	reads     []entities.Hash
	searches  []string
}

func (s *mountTestStore) GetObject(ctx context.Context, key entities.Hash) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopReads {
		return s.CASStore.GetObject(ctx, key)
	}
	s.reads = append(s.reads, key)
	return nil, entities.NotExistErrf("object %s not served in this test", key)
}

func (s *mountTestStore) SearchRefs(server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	s.mu.Lock()
	s.searches = append(s.searches, refPrefix)
	s.mu.Unlock()
	return s.CASStore.SearchRefs(server, project, refPrefix, limit)
}

// testMountSetup runs the manifest steps of a classic mount without mounting: setupRepo as
// NewABFS does, then createManifestProject as Serve does. It returns the commit
// createManifestProject took and the refs searched from there on, or the setup error.
func testMountSetup(t *testing.T, c *client.ABFSClient, info ABFSInfo) (entities.Hash, []string, error) {
	info.ManifestServer = testManifestServer
	info.ManifestProjectName = testManifestProject
	info.ManifestScheme = "https"
	info.ManifestBranchFile = "default.xml"
	s := &mountTestStore{CASStore: remote.NewRemoteStore(c)}
	a := &ABFS{ABFSInfo: info, store: s, git: casgit.New(s)}
	if err := a.setupRepo(); err != nil {
		return entities.Hash{}, nil, err
	}
	s.mu.Lock()
	s.stopReads = true
	s.searches = nil
	s.mu.Unlock()

	require.NotPanics(t, func() {
		err := a.createManifestProject(context.Background(), nil)
		require.Error(t, err, "the test store fails every object read")
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.reads {
		if !h.IsZero() {
			return h, s.searches, nil
		}
	}
	require.FailNow(t, "createManifestProject read no commit")
	return entities.Hash{}, nil, nil
}

func TestCreateManifestProject_UsesResolvedCommit(t *testing.T) {
	c, commits := newTestManifestServer(t, map[string]string{
		"refs/heads/main":  "on-main",
		"refs/heads/rel/a": "on-rel-a",
		"refs/heads/zz":    "on-zz",
		"refs/tags/v1":     "on-tag-v1",
	})
	manifestBytes := []byte(`<manifest/>`)
	tests := []struct {
		name string
		info ABFSInfo
		want string
	}{
		{name: "branch", info: ABFSInfo{ManifestBranch: "main"}, want: "on-main"},
		{name: "branch with slash", info: ABFSInfo{ManifestBranch: "rel/a"}, want: "on-rel-a"},
		{name: "file and branch", info: ABFSInfo{ManifestBytes: manifestBytes, ManifestBranch: "main"}, want: "on-main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			picked, searches, err := testMountSetup(t, c, tt.info)
			require.NoError(t, err)
			require.Equal(t, commits[tt.want], picked)
			require.Empty(t, searches, "createManifestProject searched the refs again")
		})
	}
}

func TestSetupRepo_ManifestBranchMustBeBranch(t *testing.T) {
	c, commits := newTestManifestServer(t, map[string]string{
		"refs/heads/main":      "on-main",
		"refs/heads/rel/a":     "on-rel-a",
		"refs/heads/rel/b":     "on-rel-b",
		"refs/heads/release-1": "on-release-1",
		"refs/tags/v1":         "on-tag-v1",
	})
	sources := []struct {
		name string
		info ABFSInfo
	}{
		{name: "branch only"},
		{name: "file", info: ABFSInfo{ManifestBytes: []byte(`<manifest/>`)}},
	}
	tests := []struct {
		branch  string
		wantErr string
	}{
		{branch: "rel"},
		{branch: "release"},
		{branch: "nope"},
		{branch: "v1", wantErr: "only a tag of that name"},
		{branch: "refs/heads/main"},
		{branch: "refs/tags/v1"},
		{branch: "refs/heads"},
		{branch: commits["on-main"].String()},
	}
	for _, src := range sources {
		for _, tt := range tests {
			t.Run(src.name+"/"+tt.branch, func(t *testing.T) {
				info := src.info
				info.ManifestBranch = tt.branch
				var err error
				require.NotPanics(t, func() { _, _, err = testMountSetup(t, c, info) })
				require.ErrorContains(t, err, fmt.Sprintf("%q", tt.branch), "a name that is not a branch must fail before the mount")
				require.ErrorContains(t, err, "--branch takes a branch name")
				if tt.wantErr != "" {
					require.ErrorContains(t, err, tt.wantErr)
				}
			})
		}
	}
}

func TestCreateManifestProject_NoBranchUsesHead(t *testing.T) {
	refs := map[string]string{
		"refs/heads/main": "on-main",
		"refs/heads/zz":   "on-zz",
	}
	manifestBytes := []byte(`<manifest/>`)
	tests := []struct {
		name string
		head string
		want string
	}{
		{name: "head", head: "refs/heads/main", want: "on-main"},
		{name: "no head", want: "on-zz"},
		{name: "head to a missing branch", head: "refs/heads/gone", want: "on-zz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, commits := newTestManifestServer(t, refs)
			if tt.head != "" {
				putTestHead(t, c, tt.head)
			}
			picked, _, err := testMountSetup(t, c, ABFSInfo{ManifestBytes: manifestBytes})
			require.NoError(t, err)
			require.Equal(t, commits[tt.want], picked)
		})
	}

	t.Run("no branches", func(t *testing.T) {
		c, _ := newTestManifestServer(t, map[string]string{"refs/tags/v1": "on-tag-v1"})
		var err error
		require.NotPanics(t, func() { _, _, err = testMountSetup(t, c, ABFSInfo{ManifestBytes: manifestBytes}) })
		require.ErrorContains(t, err, "no branches")
	})
}
