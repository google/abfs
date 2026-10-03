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
	"fmt"
	"testing"

	"github.com/google/abfs/manifest"
	"github.com/stretchr/testify/require"
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
