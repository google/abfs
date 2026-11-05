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

func TestFlattenIncludes_GroupsAndRevision(t *testing.T) {
	files := map[string]string{
		"top.xml": `<manifest>
 <remote name="origin" fetch=".." revision="remote-rev"></remote>
 <default remote="origin" revision="main"></default>
 <project name="p/top"></project>
 <include name="a.xml" groups="g1"></include>
 <include name="r.xml" revision="include-rev"></include>
</manifest>`,
		"a.xml": `<manifest>
 <project name="p/a"></project>
 <include name="b.xml" groups="g2" revision="r2"></include>
</manifest>`,
		"b.xml": `<manifest>
 <project name="p/b" groups="own"></project>
 <extend-project name="p/a" groups="x"></extend-project>
 <include name="c.xml"></include>
</manifest>`,
		"c.xml": `<manifest>
 <project name="p/c"></project>
 <project name="p/d" revision="mine"></project>
</manifest>`,
		"r.xml": `<manifest>
 <project name="p/r"></project>
</manifest>`,
	}
	bytes := func(ctx context.Context, name string) ([]byte, error) {
		return []byte(files[name]), nil
	}

	m, err := Parse([]byte(files["top.xml"]))
	require.NoError(t, err)
	require.NoError(t, m.FlattenIncludes(context.Background(), bytes))
	require.NoError(t, m.Validate())

	type got struct {
		Groups       []string
		GroupsString string
		Revision     string
	}
	projects := make(map[string]got)
	for _, p := range m.Project {
		projects[p.Name] = got{p.Groups, p.GroupsString, p.Revision}
	}
	require.Equal(t, map[string]got{
		"p/top": {},
		"p/a":   {[]string{"g1"}, "g1", ""},
		"p/b":   {[]string{"g1", "g2", "own"}, "g1,g2,own", "r2"},
		"p/c":   {[]string{"g1", "g2"}, "g1,g2", "r2"},
		"p/d":   {[]string{"g1", "g2"}, "g1,g2", "mine"},
		"p/r":   {nil, "", "include-rev"},
	}, projects)
	require.Equal(t, []string{"g1", "g2", "x"}, m.ExtendProject[0].Groups)
	require.Equal(t, "g1,g2,x", m.ExtendProject[0].GroupsString)
}

// Included files may repeat the default and the remotes, as in repo, but not change them.
func TestFlattenIncludes_RepeatedDefaultAndRemote(t *testing.T) {
	top := `<manifest>
 <remote name="aosp" fetch=".." review="https://review.example.com"></remote>
 <default remote="aosp" revision="main"></default>
 <include name="inc.xml"></include>
</manifest>`
	tests := []struct {
		name    string
		inc     string
		wantErr string
	}{
		{name: "equal default", inc: `<default remote="aosp" revision="main"></default>`},
		{name: "empty default", inc: `<default></default>`},
		{name: "different default", inc: `<default remote="aosp" revision="other"></default>`, wantErr: "default element already exists"},
		{name: "equal remote", inc: `<remote name="aosp" fetch=".." review="https://review.example.com"></remote>`},
		{name: "different remote", inc: `<remote name="aosp" fetch=".." review="https://other.example.com"></remote>`, wantErr: "remote aosp already exists with different attributes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bytes := func(ctx context.Context, name string) ([]byte, error) {
				return []byte("<manifest>\n " + tt.inc + "\n <project name=\"p/one\"></project>\n</manifest>"), nil
			}
			m, err := Parse([]byte(top))
			require.NoError(t, err)
			err = m.FlattenIncludes(context.Background(), bytes)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NoError(t, m.Validate())
			require.Equal(t, "main", m.Default.Revision)
			require.Len(t, m.Project, 1)
		})
	}
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
