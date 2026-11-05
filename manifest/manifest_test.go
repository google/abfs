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

package manifest

import (
	"context"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXMLMarshal(t *testing.T) {
	m := Manifest{}
	buf, err := xml.MarshalIndent(m, "", " ")
	require.NoError(t, err)
	expected := `
<manifest></manifest>`
	require.Equal(t, strings.TrimSpace(expected), string(buf))
}

func TestRemoveProjects(t *testing.T) {
	in := `<manifest>
 <remote name="aosp" fetch=".." review="https://android-review.googlesource.com/"></remote>
 <project name="platform/build/blueprint" path="build/blueprint1"></project>
 <project name="platform/build/soong" path="build/soong"></project>
 <project name="platform/build/blueprint" path="build/blueprint2"></project>
 <remove-project name="platform/build/blueprint"></remove-project>
 <project name="platform/build/blueprint" path="build/blueprint3"></project>
 <project name="platform/art" path="art"></project>
</manifest>`

	want := []*Project{
		{
			XMLName: xml.Name{Local: "project"},
			Name:    "platform/build/soong",
			Path:    "build/soong",
		},
		{
			XMLName: xml.Name{Local: "project"},
			Name:    "platform/build/blueprint",
			Path:    "build/blueprint3",
		},
		{
			XMLName: xml.Name{Local: "project"},
			Name:    "platform/art",
			Path:    "art",
		},
	}
	m, err := Parse([]byte(in))
	require.NoError(t, err)
	projects, err := m.ApplyRemoves()
	require.NoError(t, err)
	require.Equal(t, want, projects)
	require.NoError(t, m.Validate())
}

// A parsed manifest marshals back with every attribute and child it was read with.
func TestRawManifestRoundTrip(t *testing.T) {
	in := `<manifest>
 <remote name="aosp" fetch=".."></remote>
 <default remote="aosp" revision="main"></default>
 <include name="extra.xml" groups="extra" revision="v1"></include>
 <project name="p/one" path="one" groups="a,b"></project>
 <project name="p/two" path="two"></project>
 <remove-project path="one" optional="true" base-rev="main"></remove-project>
 <extend-project name="p/two" path="two" groups="eg" revision="v2" remote="aosp" dest-path="moved" dest-branch="dev" upstream="up" base-rev="main">
  <annotation name="k" value="v"></annotation>
  <copyfile src="a" dest="b"></copyfile>
  <linkfile src="c" dest="d"></linkfile>
 </extend-project>
</manifest>`

	m, err := Parse([]byte(in))
	require.NoError(t, err)
	got, err := xml.MarshalIndent(m, "", " ")
	require.NoError(t, err)
	require.Equal(t, in, string(got))

	again, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, m.Ordered, again.Ordered)
	require.NoError(t, again.Validate())
}

func TestParseGroups(t *testing.T) {
	in := `<manifest>
 <project name="p/one" groups="a, b,,a	c"></project>
 <project name="p/two"></project>
 <extend-project name="p/one" groups=" d,e  d"></extend-project>
</manifest>`

	m, err := Parse([]byte(in))
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, m.Project[0].Groups)
	require.Empty(t, m.Project[1].Groups)
	require.Equal(t, []string{"d", "e"}, m.ExtendProject[0].Groups)
}

// After cloning a manifest, the order of elements should be identical.
func TestCloneOrder(t *testing.T) {
	in := `<manifest>
 <notice>Hello Notice</notice>
 <remote name="aosp" fetch=".." review="https://android-review.googlesource.com/"></remote>
 <project name="platform/build/blueprint" path="build/blueprint1"></project>
 <remove-project name="platform/build/blueprint"></remove-project>
</manifest>`

	m, err := Parse([]byte(in))
	require.NoError(t, err)
	cloned, err := CloneManifest(m)
	require.NoError(t, err)
	require.Equal(t, m.Ordered, cloned.Ordered)
	require.NoError(t, m.Validate())
}

func TestFlattenIncludes(t *testing.T) {
	top := `<manifest>
 <notice>Hello Notice</notice>
 <remote name="aosp" fetch=".." review="ssh://user@host:29418"></remote>
 <project name="platform/build/blueprint" path="build/blueprint1"></project>
 <include name="one"></include>
 <project name="platform/build/soong" path="build/soong"></project>
</manifest>`

	want := `<manifest>
 <notice>Hello Notice</notice>
 <remote name="aosp" fetch=".." review="ssh://user@host:29418"></remote>
 <project name="platform/build/blueprint" path="build/blueprint1"></project>
 <remove-project name="platform/build/blueprint"></remove-project>
 <project name="platform/build/blueprint" path="build/blueprint2"></project>
 <project name="platform/build/soong" path="build/soong"></project>
</manifest>`

	m, err := Parse([]byte(top))
	require.NoError(t, err)
	m.FlattenIncludes(context.Background(), manifestBytes)
	require.NoError(t, m.Validate())

	got, err := xml.MarshalIndent(m, "", " ")
	require.NoError(t, err)
	require.Equal(t, want, string(got))
}

func TestFilter(t *testing.T) {
	in := `<manifest>
 <notice>Hello Notice</notice>
 <remote name="aosp" fetch=".." review="https://android-review.googlesource.com/"></remote>
 <project name="platform/build/blueprint" path="build/blueprint1" groups="notdefault"></project>
 <project name="platform/build/soong" path="build/soong"></project>
 <remove-project name="platform/build/blueprint"></remove-project>
 <project name="platform/build/blueprint" path="build/blueprint2"></project>
 <remove-project name="platform/build/blueprint"></remove-project>
 <project name="platform/build/blueprint" path="build/blueprint3"></project>
</manifest>`

	want := `<manifest>
 <notice>Hello Notice</notice>
 <remote name="aosp" fetch=".." review="https://android-review.googlesource.com/"></remote>
 <project name="platform/build/soong" path="build/soong"></project>
 <project name="platform/build/blueprint" path="build/blueprint2"></project>
 <remove-project name="platform/build/blueprint"></remove-project>
 <project name="platform/build/blueprint" path="build/blueprint3"></project>
</manifest>`

	m, err := Parse([]byte(in))
	require.NoError(t, err)
	m.Filter()
	got, err := xml.MarshalIndent(m, "", " ")
	require.NoError(t, err)
	require.Equal(t, want, string(got))
	require.NoError(t, m.Validate())
}

func manifestBytes(ctx context.Context, name string) ([]byte, error) {
	switch name {
	case "top":
		return []byte(`<manifest>
 <notice>Hello Notice</notice>
 <remote name="aosp" fetch=".." review="ssh://user@host:29418"></remote>
 <project name="platform/build/blueprint" path="build/blueprint1"></project>
 <include name="one"></include>
 <project name="platform/build/soong" path="build/soong"></project>
</manifest>
`), nil
	case "one":
		return []byte(`<manifest>
 <remove-project name="platform/build/blueprint"></remove-project>
 <include name="two"></include>
</manifest>
`), nil
	case "two":
		return []byte(`<manifest>
 <project name="platform/build/blueprint" path="build/blueprint2"></project>
</manifest>
`), nil
	default:
		return nil, nil
	}
}

func TestAddNotice(t *testing.T) {
	in := `<manifest>
 <notice>Hello Notice</notice>
</manifest>`

	m, err := Parse([]byte(in))
	require.NoError(t, err)
	require.Error(t, m.AddNotice(new(Notice)))

	in = `<manifest>
</manifest>`
	m, err = Parse([]byte(in))
	require.NoError(t, err)
	require.NoError(t, m.AddNotice(new(Notice)))
}

func TestExpandFetch(t *testing.T) {
	tests := [][]string{
		{"..", "https://android.googlesource.com/platform/manifest"},
		{"../aosp", "https://android.googlesource.com/platform/manifest"},
		{"../../aosp", "https://android.googlesource.com/platform/manifests/release"},
		{"https://android.googlesource.com/foobar", "https://android.googlesource.com/platform/manifest"},
	}
	want := []string{
		"https://android.googlesource.com/",
		"https://android.googlesource.com/aosp",
		"https://android.googlesource.com/aosp",
		"https://android.googlesource.com/foobar",
	}
	for idx, test := range tests {
		got, err := ExpandFetch(test[0], test[1])
		require.NoError(t, err)
		require.Equal(t, want[idx], got)
	}
}
