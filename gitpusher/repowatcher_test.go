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

package gitpusher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/manifest"
)

func TestForAllManifests(t *testing.T) {
	require := require.New(t)

	contentor := &testContentor{}
	// DFS pre-order traversal
	want := []string{
		"goog",
		"r0_0_remote",
		"r1_0_remote",
		"r0_1_remote",
	}
	var got []string
	err := forAllManifests(contentor, "default.xml", func(m *manifest.Manifest) error {
		got = append(got, m.Remote[0].Name)
		return nil
	})
	require.NoError(err)
	require.Equal(want, got)
}

func TestProjectMappings(t *testing.T) {
	require := require.New(t)

	got, err := projectMappingsForFile(&testContentor{}, "https://android.com/platform/manifest", "default.xml")
	require.NoError(err)
	require.Equal("goog", got.defaultRemote)

	wantRemoteURLs := map[string]string{
		"goog":        "https://android.com/",
		"r0_0_remote": "https://anotherhost.com/r0_0",
		"r0_1_remote": "https://android.com/",
		"r1_0_remote": "https://android.com/",
	}
	for wantName, wantURL := range wantRemoteURLs {
		require.Equal(wantURL, got.remotes[wantName])
	}

	wantProjectRemotes := []projectRemote{
		{
			name:   "default",
			remote: "",
		},
		{
			name:   "r0_0",
			remote: "r0_0_remote",
		},
		{
			name:   "r0_1",
			remote: "",
		},
		{
			name:   "r1_0",
			remote: "",
		},
	}
	for _, want := range wantProjectRemotes {
		if _, found := got.projectRemotes[want]; !found {
			require.Fail("Did not find expected project", want)
		}
	}
}

func TestDuplicateDefault(t *testing.T) {
	require := require.New(t)
	_, err := projectMappingsForFile(&testContentorDuplicateDefault{}, "https://android.com/platform/manifest", "default.xml")
	require.Error(err)
}

func TestDuplicateRemote(t *testing.T) {
	require := require.New(t)
	_, err := projectMappingsForFile(&testContentorDuplicateRemote{}, "https://android.com/platform/manifest", "default.xml")
	require.Error(err)
}

type testContentor struct {
}

func (t *testContentor) Contents(manifestFile string) (string, error) {
	switch manifestFile {
	case "default.xml":
		return `<manifest>
  <remote fetch=".." name="goog" />
  <default dest-branch="main" remote="goog" revision="main" />
  <project name="default" />
  <include name="r0_0.xml"/>
  <include name="r0_1.xml"/>
</manifest>
	}`, nil
	case "r0_0.xml":
		return `<manifest>
  <remote fetch="https://anotherhost.com/r0_0" name="r0_0_remote" />
  <project name="r0_0" remote="r0_0_remote" />
  <include name="r1_0.xml"/>
</manifest>
	}`, nil
	case "r0_1.xml":
		return `<manifest>
  <remote fetch=".." name="r0_1_remote" />
  <project name="r0_1" />
</manifest>
	}`, nil
	case "r1_0.xml": // this one purposely has a cycle
		return `<manifest>
  <remote fetch=".." name="r1_0_remote" />
  <project name="r1_0" />
  <include name="r1_0.xml"/>
</manifest>
	}`, nil
	default:
		return "", fmt.Errorf("unsupported file %q", manifestFile)
	}
}

type testContentorDuplicateDefault struct {
}

func (t *testContentorDuplicateDefault) Contents(manifestFile string) (string, error) {
	switch manifestFile {
	case "default.xml":
		return `<manifest>
  <remote fetch=".." name="goog" />
  <default dest-branch="main" remote="goog" revision="main" />
  <include name="r0_0.xml"/>
</manifest>
	}`, nil
	case "r0_0.xml":
		return `<manifest>
  <remote fetch=".." name="aosp" />
  <default dest-branch="main" remote="aosp" revision="main" />
</manifest>
	}`, nil
	default:
		return "", fmt.Errorf("unsupported file %q", manifestFile)
	}
}

type testContentorDuplicateRemote struct {
}

func (t *testContentorDuplicateRemote) Contents(manifestFile string) (string, error) {
	switch manifestFile {
	case "default.xml":
		return `<manifest>
  <remote fetch=".." name="goog" />
  <default dest-branch="main" remote="goog" revision="main" />
  <include name="r0_0.xml"/>
</manifest>
	}`, nil
	case "r0_0.xml":
		return `<manifest>
  <remote fetch="../bar" name="goog" />
</manifest>
	}`, nil
	default:
		return "", fmt.Errorf("unsupported file %q", manifestFile)
	}
}

type testContentorDuplicateIncludes struct {
}

func (t *testContentorDuplicateIncludes) Contents(manifestFile string) (string, error) {
	switch manifestFile {
	case "default.xml":
		return `<manifest>
  <remote fetch=".." name="goog" />
  <default dest-branch="main" remote="goog" revision="main" />
  <project name="default" />
  <include name="r0_0.xml"/>
  <include name="r0_0.xml"/>
</manifest>`, nil
	case "r0_0.xml":
		return `<manifest>
</manifest>`, nil
	default:
		return "", fmt.Errorf("unsupported file %q", manifestFile)
	}
}

func TestForAllManifests_DuplicateIncludesInfiniteLoop(t *testing.T) {
	require := require.New(t)

	contentor := &testContentorDuplicateIncludes{}
	err := forAllManifests(contentor, "default.xml", func(m *manifest.Manifest) error {
		return nil
	})
	require.NoError(err)
}

type testContentorCycle struct {
}

func (t *testContentorCycle) Contents(manifestFile string) (string, error) {
	switch manifestFile {
	case "default.xml":
		return `<manifest>
  <remote fetch=".." name="goog" />
  <default dest-branch="main" remote="goog" revision="main" />
  <project name="default" />
  <include name="r0_0.xml"/>
  <include name="r0_1.xml"/>
</manifest>`, nil
	case "r0_0.xml":
		return `<manifest>
	<include name="default.xml"/>
  <include name="r1_0.xml"/>
</manifest>`, nil
	case "r0_1.xml":
		return `<manifest></manifest>`, nil
	case "r1_0.xml":
		return `<manifest></manifest>`, nil
	default:
		return "", fmt.Errorf("unsupported file %q", manifestFile)
	}
}

func TestForAllManifests_CycleInfiniteLoop(t *testing.T) {
	require := require.New(t)

	contentor := &testContentorCycle{}
	err := forAllManifests(contentor, "default.xml", func(m *manifest.Manifest) error {
		return nil
	})
	require.NoError(err)
}

type testContentorRemotesWithPath struct {
}

func (t *testContentorRemotesWithPath) Contents(manifestFile string) (string, error) {
	switch manifestFile {
	case "default.xml":
		return `<manifest>
  <remote fetch="https://gerrit.example.com/a/" name="gerrit" />
  <remote fetch="https://github.com/google/" name="github" />
  <default dest-branch="main" remote="gerrit" revision="main" />
  <project name="platform/foo" />
  <project name="uuid" remote="github" />
</manifest>`, nil
	default:
		return "", fmt.Errorf("unsupported file %q", manifestFile)
	}
}

func TestProjectRemoteURLSet_RemotesWithPath(t *testing.T) {
	require := require.New(t)

	mappings, err := projectMappingsForFile(&testContentorRemotesWithPath{}, "https://gerrit.example.com/a/platform/manifest", "default.xml")
	require.NoError(err)
	set := make(map[projectRemoteSchemeURL]struct{})
	require.NoError(mappings.updateProjectRemoteURLSet(set))

	// Clients look refs up under the host of the remote and the manifest name, so the
	// path of the remote stays in the fetch URL only.
	want := map[projectRemoteSchemeURL]struct{}{
		{name: "platform/foo", server: "gerrit.example.com", remote: "gerrit", scheme: "https", url: "gerrit.example.com/a/platform/foo"}: {},
		{name: "uuid", server: "github.com", remote: "github", scheme: "https", url: "github.com/google/uuid"}:                            {},
	}
	require.Equal(want, set)
}

// manifestUpstream is a manifest repository that a test changes between listings.
type manifestUpstream struct {
	t        *testing.T
	dir      string
	worktree string
}

func newManifestUpstream(t *testing.T) *manifestUpstream {
	u := &manifestUpstream{t: t, dir: filepath.Join(t.TempDir(), "manifest.git"), worktree: t.TempDir()}
	u.git(t.TempDir(), "init", "-q", "--bare", u.dir)
	u.git(u.worktree, "init", "-q")
	return u
}

func (u *manifestUpstream) git(dir string, args ...string) {
	cmd := exec.Command("git", append([]string{"-c", "safe.bareRepository=all", "-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(u.t, err, "git %v failed: %s", args, out)
}

// commit sets branch to a commit that holds exactly files.
func (u *manifestUpstream) commit(branch string, files map[string]string) {
	entries, err := os.ReadDir(u.worktree)
	require.NoError(u.t, err)
	for _, e := range entries {
		if e.Name() != ".git" {
			require.NoError(u.t, os.RemoveAll(filepath.Join(u.worktree, e.Name())))
		}
	}
	for name, content := range files {
		require.NoError(u.t, os.WriteFile(filepath.Join(u.worktree, name), []byte(content), 0644))
	}
	u.git(u.worktree, "add", "-A")
	u.git(u.worktree, "commit", "-q", "--allow-empty", "-m", branch)
	u.git(u.worktree, "push", "-q", "-f", u.dir, "HEAD:refs/heads/"+branch)
}

func manifestXML(fetch string, projects ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<manifest>\n  <remote name=\"aosp\" fetch=%q />\n  <default remote=\"aosp\" revision=\"main\" />\n", fetch)
	for _, p := range projects {
		fmt.Fprintf(&b, "  <project name=%q />\n", p)
	}
	b.WriteString("</manifest>\n")
	return b.String()
}

// newTestRepoWatcher watches u. git reads file://localhost/<path> as a local path, and the host
// lets the URL pass the same validation as a real manifest URL.
func newTestRepoWatcher(t *testing.T, u *manifestUpstream, files ...ManifestFile) *RepoWatcher {
	config := &RepoConfig{Manifest: ManifestConfig{
		URL:     "file://localhost" + u.dir,
		Server:  "example.com",
		Project: "platform/manifest",
		Files:   files,
	}}
	require.NoError(t, config.Validate())
	g := &GitPusher{projectStoragePath: t.TempDir(), shgit: shellgit.NewShellGit("/usr/bin/git")}
	return newRepoWatcher(config, g).(*RepoWatcher)
}

func projectURLs(projects []*ProjectConfig) []string {
	var urls []string
	for _, p := range projects {
		urls = append(urls, p.URL)
	}
	slices.Sort(urls)
	return urls
}

const manifestProjectURL = "file://localhost"

func listProjectURLs(t *testing.T, w *RepoWatcher) []string {
	projects, err := w.ListProjects(context.Background())
	require.NoError(t, err)
	var urls []string
	for _, url := range projectURLs(projects) {
		if !strings.HasPrefix(url, manifestProjectURL) {
			urls = append(urls, url)
		}
	}
	return urls
}

func TestListProjects_BranchPattern(t *testing.T) {
	u := newManifestUpstream(t)
	u.commit("main", map[string]string{"default.xml": manifestXML("https://example.com", "main-only")})
	u.commit("release/a", map[string]string{"default.xml": manifestXML("https://example.com", "a-only")})
	w := newTestRepoWatcher(t, u, ManifestFile{Branch: "release/*", File: "default.xml"})

	require.Equal(t, []string{"https://example.com/a-only"}, listProjectURLs(t, w))

	// A new branch is found by the next listing, without a config change.
	u.commit("release/b/1", map[string]string{"default.xml": manifestXML("https://example.com", "b-only")})
	require.Equal(t, []string{"https://example.com/a-only", "https://example.com/b-only"}, listProjectURLs(t, w))
}

func TestListProjects_FileNamePerBranch(t *testing.T) {
	u := newManifestUpstream(t)
	u.commit("release/a", map[string]string{"default.xml": manifestXML("https://example.com", "a-only")})
	u.commit("release/x", map[string]string{"other.xml": manifestXML("https://example.com", "x-only")})
	w := newTestRepoWatcher(t, u,
		ManifestFile{Branch: "release/*", File: "default.xml"},
		ManifestFile{Branch: "release/*", File: "other.xml"})

	require.Equal(t, []string{"https://example.com/a-only", "https://example.com/x-only"}, listProjectURLs(t, w))
	require.Equal(t, []string{"release/a:default.xml", "release/x:other.xml"}, w.lastReport.read)
	require.Equal(t, []string{"release/x:default.xml: no such file", "release/a:other.xml: no such file"}, w.lastReport.skipped)
}

func TestListProjects_SameProjectFromTwoBranches(t *testing.T) {
	u := newManifestUpstream(t)
	u.commit("b", map[string]string{"default.xml": manifestXML("https://example.com/mirror", "shared")})
	u.commit("a", map[string]string{"default.xml": manifestXML("https://example.com", "shared")})
	u.commit("c", map[string]string{"default.xml": strings.Replace(manifestXML("https://example.com", "shared"), `"aosp"`, `"other"`, 2)})
	w := newTestRepoWatcher(t, u,
		ManifestFile{Branch: "a", File: "default.xml"},
		ManifestFile{Branch: "b", File: "default.xml"},
		ManifestFile{Branch: "c", File: "default.xml"})

	// One config per project name, from the first manifest file entry, on every listing.
	for range 3 {
		require.Equal(t, []string{"https://example.com/shared"}, listProjectURLs(t, w))
	}
	require.Equal(t, []string{"b:default.xml: project example.com/shared also listed as https://example.com/mirror/shared"}, w.lastReport.skipped)
}
