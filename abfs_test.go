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
	"context"
	"testing"

	"github.com/google/abfs/manifest"
	"github.com/stretchr/testify/assert"

	"github.com/google/abfs/casgit"
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
