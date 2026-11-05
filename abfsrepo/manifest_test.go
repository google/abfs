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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

const masterManifest = `<manifest>
  <include name="default.xml" />
</manifest>
`

// newTestRepo writes the master manifest and the given files under .repo/manifests.
func newTestRepo(t *testing.T, files map[string]string) *Repo {
	t.Helper()
	root := t.TempDir()
	manifests := filepath.Join(root, ".repo", "manifests")
	require.NoError(t, os.MkdirAll(manifests, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".repo", "manifest.xml"), []byte(masterManifest), 0644))
	for name, content := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(manifests, name)), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(manifests, name), []byte(content), 0644))
	}
	return NewRepoForceRoot(RepoOpts{}, root)
}

func projectGroups(r *Repo) map[string][]string {
	groups := make(map[string][]string)
	for _, p := range r.flatMan.Project {
		groups[p.GetPath()] = p.Groups
	}
	return groups
}

func TestLoadManifest_SmartSyncOverrideGroups(t *testing.T) {
	r := newTestRepo(t, map[string]string{
		"default.xml": `<manifest>
  <remote name="origin" fetch="https://example.com" />
  <default revision="main" remote="origin" />
  <project name="p/one" path="one" />
</manifest>
`,
		"smart_sync_override.xml": `<manifest>
  <remote name="origin" fetch="https://example.com" />
  <default revision="main" remote="origin" />
  <project name="p/one" path="one" groups="g1, g2" revision="0123456789012345678901234567890123456789" />
  <project name="p/two" path="two" revision="0123456789012345678901234567890123456789" />
</manifest>
`,
	})

	require.NoError(t, r.loadManifest(context.Background()))
	require.Equal(t, map[string][]string{
		"one": {"g1", "g2"},
		"two": nil,
	}, projectGroups(r))
}

func selectedPaths(r *Repo, groups ...string) []string {
	r.groupsInclude, r.groupsExclude = FilterGroups(groups)
	r.filterProjectsByGroups()
	return slices.Sorted(maps.Keys(r.projectsByPath))
}

func TestLoadManifest_IncludeGroups(t *testing.T) {
	r := newTestRepo(t, map[string]string{
		"default.xml": `<manifest>
  <remote name="origin" fetch="https://example.com" />
  <default revision="main" remote="origin" />
  <project name="p/one" path="one" />
  <include name="extra.xml" groups="extra" />
</manifest>
`,
		"extra.xml": `<manifest>
  <project name="p/two" path="two" />
</manifest>
`,
	})

	require.NoError(t, r.loadManifest(context.Background()))
	require.Equal(t, []string{"one", "two"}, selectedPaths(r))
	require.Equal(t, []string{"two"}, selectedPaths(r, "extra"))
}
