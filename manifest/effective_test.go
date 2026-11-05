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
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// testManifest wraps body in a manifest with two remotes and a default.
func testManifest(body string) string {
	return `<manifest>
 <remote name="aosp" fetch=".."></remote>
 <remote name="partner" fetch="https://partner.example.com" revision="partner-rev"></remote>
 <default remote="aosp" revision="main"></default>
` + body + `
</manifest>`
}

// base lists one name at two paths and a second project.
const base = `
 <project name="platform/external/uuid" path="ws/a-uuid" revision="v1.3.0"></project>
 <project name="platform/external/uuid" path="ws/b-uuid" revision="v1.3.0"></project>
 <project name="platform/external/cmp" path="ws/cmp" revision="v0.5.9"></project>`

// projectLines describes each project as path, name, revision after inheritance, remote and groups.
func projectLines(m *Manifest) []string {
	var lines []string
	for _, p := range m.Project {
		remote := p.Remote
		if remote == "" {
			remote = m.Default.Remote
		}
		groups := slices.Sorted(slices.Values(p.Groups))
		lines = append(lines, fmt.Sprintf("%s %s %s %s %v", p.GetPath(), p.Name, m.effectiveRevision(p), remote, groups))
	}
	return lines
}

func TestEffective(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []string
		wantErr string
	}{
		{
			name: "one name at two paths",
			body: base,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/cmp platform/external/cmp v0.5.9 aosp []",
			},
		},
		{
			name: "name and path remove takes one path",
			body: base + `
 <remove-project name="platform/external/uuid" path="ws/b-uuid" optional="true"/>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/cmp platform/external/cmp v0.5.9 aosp []",
			},
		},
		{
			name: "name and path removes take both paths",
			body: base + `
 <remove-project name="platform/external/uuid" path="ws/a-uuid" optional="true"></remove-project>
 <remove-project name="platform/external/uuid" path="ws/b-uuid" optional="true"/>`,
			want: []string{"ws/cmp platform/external/cmp v0.5.9 aosp []"},
		},
		{
			name: "path remove",
			body: base + `
 <remove-project path="ws/cmp"/>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
			},
		},
		{
			name: "name remove takes every path",
			body: base + `
 <remove-project name="platform/external/uuid"/>`,
			want: []string{"ws/cmp platform/external/cmp v0.5.9 aosp []"},
		},
		{
			name: "name and path remove with another path removes nothing",
			body: base + `
 <remove-project name="platform/external/uuid" path="ws/cmp" optional="true"/>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/cmp platform/external/cmp v0.5.9 aosp []",
			},
		},
		{
			name: "name and path remove with another path fails without optional",
			body: base + `
 <remove-project name="platform/external/uuid" path="ws/cmp"/>`,
			wantErr: `remove-project element specifies non-existent project: <remove-project name="platform/external/uuid" path="ws/cmp"/>`,
		},
		{
			name: "optional remove of an unknown name",
			body: base + `
 <remove-project name="platform/external/none" optional="true"/>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/cmp platform/external/cmp v0.5.9 aosp []",
			},
		},
		{
			name: "second name remove fails",
			body: base + `
 <remove-project name="platform/external/cmp"></remove-project>
 <remove-project name="platform/external/cmp"/>`,
			wantErr: `remove-project element specifies non-existent project: <remove-project name="platform/external/cmp"/>`,
		},
		{
			name: "second name remove with optional",
			body: base + `
 <remove-project name="platform/external/cmp"></remove-project>
 <remove-project name="platform/external/cmp" optional="true"/>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
			},
		},
		{
			name: "name remove after path removes emptied the name fails",
			body: base + `
 <remove-project path="ws/a-uuid"></remove-project>
 <remove-project path="ws/b-uuid"></remove-project>
 <remove-project name="platform/external/uuid"/>`,
			wantErr: `remove-project element specifies non-existent project: <remove-project name="platform/external/uuid"/>`,
		},
		{
			name: "remove without name and path",
			body: base + `
 <remove-project optional="true"/>`,
			wantErr: "remove-project must have name and/or path",
		},
		{
			name: "remove then add at the same path",
			body: base + `
 <remove-project name="platform/external/cmp"></remove-project>
 <project name="platform/external/cmp" path="ws/cmp" revision="v0.6.0"></project>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/cmp platform/external/cmp v0.6.0 aosp []",
			},
		},
		{
			name: "duplicate path",
			body: base + `
 <project name="platform/external/other" path="ws/cmp"></project>`,
			wantErr: "duplicate path ws/cmp",
		},
		{
			name: "extend revision",
			body: base + `
 <extend-project name="platform/external/cmp" revision="v0.6.0"></extend-project>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/cmp platform/external/cmp v0.6.0 aosp []",
			},
		},
		{
			name: "extend revision of one path",
			body: base + `
 <extend-project name="platform/external/uuid" path="ws/b-uuid" revision="v1.6.0"></extend-project>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.6.0 aosp []",
				"ws/cmp platform/external/cmp v0.5.9 aosp []",
			},
		},
		{
			name: "extend dest-path",
			body: base + `
 <extend-project name="platform/external/cmp" path="ws/cmp" dest-path="ws/moved-cmp"></extend-project>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/moved-cmp platform/external/cmp v0.5.9 aosp []",
			},
		},
		{
			name: "extend dest-path with a trailing slash",
			body: base + `
 <extend-project name="platform/external/cmp" path="ws/cmp" dest-path="ws/moved-cmp/"></extend-project>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/moved-cmp platform/external/cmp v0.5.9 aosp []",
			},
		},
		{
			name: "extend dest-path without path on a single project",
			body: base + `
 <extend-project name="platform/external/cmp" dest-path="ws/moved-cmp"></extend-project>
 <project name="platform/external/other" path="ws/cmp"></project>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/moved-cmp platform/external/cmp v0.5.9 aosp []",
				"ws/cmp platform/external/other main aosp []",
			},
		},
		{
			name: "extend dest-path without path on two paths",
			body: base + `
 <extend-project name="platform/external/uuid" dest-path="ws/moved-uuid"></extend-project>`,
			wantErr: "extend-project cannot use dest-path when matching multiple projects: platform/external/uuid",
		},
		{
			name: "extend dest-path onto a used path",
			body: base + `
 <extend-project name="platform/external/uuid" path="ws/a-uuid" dest-path="ws/cmp"></extend-project>`,
			wantErr: "extend-project platform/external/uuid cannot move ws/a-uuid to ws/cmp: the path is used by platform/external/cmp",
		},
		{
			name: "extend groups",
			body: base + `
 <extend-project name="platform/external/cmp" groups="extra, pdk"></extend-project>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/cmp platform/external/cmp v0.5.9 aosp [extra pdk]",
			},
		},
		{
			name: "extend of an unknown name",
			body: base + `
 <extend-project name="platform/external/none" revision="v1"></extend-project>`,
			wantErr: "extend-project element specifies non-existent project: platform/external/none",
		},
		{
			name: "extend of a removed name",
			body: base + `
 <remove-project name="platform/external/cmp"></remove-project>
 <extend-project name="platform/external/cmp" revision="v1"></extend-project>`,
			wantErr: "extend-project element specifies non-existent project: platform/external/cmp",
		},
		{
			name: "extend of a name emptied by path removes",
			body: base + `
 <remove-project path="ws/a-uuid"></remove-project>
 <remove-project name="platform/external/uuid" path="ws/b-uuid"></remove-project>
 <extend-project name="platform/external/uuid" revision="v1.6.0"></extend-project>`,
			want: []string{"ws/cmp platform/external/cmp v0.5.9 aosp []"},
		},
		{
			name: "extend with a path that matches nothing",
			body: base + `
 <extend-project name="platform/external/cmp" path="ws/none" revision="v0.6.0"></extend-project>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/cmp platform/external/cmp v0.5.9 aosp []",
			},
		},
		{
			name: "extend with an unknown remote",
			body: base + `
 <extend-project name="platform/external/cmp" remote="none"></extend-project>`,
			wantErr: "remote none not defined for extend-project platform/external/cmp",
		},
		{
			name: "extend remote keeps the inherited revision",
			body: `
 <project name="platform/external/cmp" path="ws/cmp"></project>
 <extend-project name="platform/external/cmp" remote="partner"></extend-project>`,
			want: []string{"ws/cmp platform/external/cmp main partner []"},
		},
		{
			name: "base-rev matches an own revision",
			body: base + `
 <extend-project name="platform/external/cmp" revision="v0.6.0" base-rev="v0.5.9"></extend-project>
 <remove-project path="ws/a-uuid" base-rev="v1.3.0"/>`,
			want: []string{
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/cmp platform/external/cmp v0.6.0 aosp []",
			},
		},
		{
			name: "base-rev matches a revision from the default",
			body: `
 <project name="platform/build/soong" path="build/soong"></project>
 <extend-project name="platform/build/soong" revision="v2" base-rev="main"></extend-project>`,
			want: []string{"build/soong platform/build/soong v2 aosp []"},
		},
		{
			name: "base-rev matches a revision from the remote",
			body: `
 <project name="platform/build/soong" path="build/soong" remote="partner"></project>
 <extend-project name="platform/build/soong" revision="v2" base-rev="partner-rev"></extend-project>`,
			want: []string{"build/soong platform/build/soong v2 partner []"},
		},
		{
			name: "base-rev compared with the default when the remote has the revision",
			body: `
 <project name="platform/build/soong" path="build/soong" remote="partner"></project>
 <extend-project name="platform/build/soong" revision="v2" base-rev="main"></extend-project>`,
			wantErr: "detected base-revision mismatch, updates needed:\nextend-project name platform/build/soong:\n  base main vs revision partner-rev",
		},
		{
			name: "base-rev without revision checks nothing",
			body: base + `
 <extend-project name="platform/external/cmp" base-rev="stale" groups="extra"></extend-project>`,
			want: []string{
				"ws/a-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/b-uuid platform/external/uuid v1.3.0 aosp []",
				"ws/cmp platform/external/cmp v0.5.9 aosp [extra]",
			},
		},
		{
			name: "base-rev mismatches are reported together",
			body: base + `
 <extend-project name="platform/external/cmp" revision="v0.6.0" base-rev="v0.4.0"></extend-project>
 <remove-project name="platform/external/uuid" base-rev="v1.0.0"></remove-project>
 <project name="platform/build/soong" path="build/soong"></project>
 <remove-project path="build/soong" base-rev="stale"/>`,
			wantErr: "detected base-revision mismatch, updates needed:\n" +
				"extend-project name platform/external/cmp:\n  base v0.4.0 vs revision v0.5.9\n" +
				"remove-project name platform/external/uuid:\n  base v1.0.0 vs revision v1.3.0\n" +
				"remove-project name platform/external/uuid:\n  base v1.0.0 vs revision v1.3.0\n" +
				"remove-project path build/soong:\n  base stale vs revision main",
		},
		{
			// The input of the former ApplyRemoves test.
			name: "name remove between adds",
			body: `
 <project name="platform/build/blueprint" path="build/blueprint1"></project>
 <project name="platform/build/soong" path="build/soong"></project>
 <project name="platform/build/blueprint" path="build/blueprint2"></project>
 <remove-project name="platform/build/blueprint"></remove-project>
 <project name="platform/build/blueprint" path="build/blueprint3"></project>
 <project name="platform/art" path="art"></project>`,
			want: []string{
				"build/soong platform/build/soong main aosp []",
				"build/blueprint3 platform/build/blueprint main aosp []",
				"art platform/art main aosp []",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := Parse([]byte(testManifest(tt.body)))
			require.NoError(t, err)
			err = m.Effective()
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, projectLines(m))
			requireProjected(t, m)
		})
	}
}

// requireProjected checks that a projected manifest is valid and marshals to itself.
func requireProjected(t *testing.T, m *Manifest) {
	t.Helper()
	require.NoError(t, m.Validate())
	require.Empty(t, m.RemoveProject)
	require.Empty(t, m.ExtendProject)
	require.Len(t, m.OrderedXProject, len(m.Project))

	b, err := xml.Marshal(m)
	require.NoError(t, err)
	again, err := Parse(b)
	require.NoError(t, err)
	require.Equal(t, projectLines(m), projectLines(again))
	require.NoError(t, again.Effective())
	require.Equal(t, projectLines(m), projectLines(again))
}

func TestEffective_ExtendAttributes(t *testing.T) {
	m, err := Parse([]byte(testManifest(base + `
 <extend-project name="platform/external/cmp" upstream="main" dest-branch="dev">
  <annotation name="k" value="v"></annotation>
  <copyfile src="LICENSE" dest="LICENSE.cmp"></copyfile>
  <linkfile src="cmp" dest="cmp-link"></linkfile>
 </extend-project>`)))
	require.NoError(t, err)
	require.NoError(t, m.Effective())
	requireProjected(t, m)

	p := m.Project[2]
	require.Equal(t, "main", p.Upstream)
	require.Equal(t, "dev", p.DestBranch)
	require.Equal(t, []*Annotation{{Name: "k", Value: "v"}}, p.Annotation)
	require.Equal(t, []*Copyfile{{Src: "LICENSE", Dest: "LICENSE.cmp"}}, p.Copyfile)
	require.Equal(t, []*Linkfile{{Src: "cmp", Dest: "cmp-link"}}, p.Linkfile)
}

func TestEffective_Includes(t *testing.T) {
	files := map[string]string{
		"default.xml": testManifest(`
 <project name="platform/external/uuid" path="ws/a-uuid"></project>
 <include name="top.xml" groups="top"></include>`),
		"top.xml": `<manifest>
 <include name="layer.xml" groups="layer" revision="top-rev"></include>
 <extend-project name="platform/external/cmp" revision="v0.6.0" base-rev="top-rev"></extend-project>
</manifest>`,
		"layer.xml": `<manifest>
 <include name="projects.xml" groups="extra"></include>
</manifest>`,
		"projects.xml": `<manifest>
 <project name="platform/external/cmp" path="ws/cmp" remote="partner"></project>
 <project name="platform/external/go" path="ws/go"></project>
 <extend-project name="platform/external/uuid" groups="own"></extend-project>
</manifest>`,
	}
	bytes := func(ctx context.Context, name string) ([]byte, error) {
		return []byte(files[name]), nil
	}

	m, err := Parse([]byte(files["default.xml"]))
	require.NoError(t, err)
	require.NoError(t, m.FlattenIncludes(context.Background(), bytes))
	require.NoError(t, m.Effective())
	requireProjected(t, m)
	require.Equal(t, []string{
		"ws/a-uuid platform/external/uuid main aosp [extra layer own top]",
		"ws/cmp platform/external/cmp v0.6.0 partner [extra layer top]",
		"ws/go platform/external/go top-rev aosp [extra layer top]",
	}, projectLines(m))
}

func TestEffective_FlattenFirst(t *testing.T) {
	m, err := Parse([]byte(testManifest(`<include name="other.xml"></include>`)))
	require.NoError(t, err)
	require.Error(t, m.Effective())
}

// A standard AOSP manifest has no removes, extends or includes, so the projection leaves it as it is.
func TestEffective_StandardManifestUnchanged(t *testing.T) {
	in := `<?xml version="1.0" encoding="UTF-8"?>
<manifest>
  <remote  name="aosp"
           fetch=".."
           review="https://android-review.googlesource.com/" />
  <default revision="refs/tags/android-15.0.0_r36"
           remote="aosp"
           sync-j="4" />
  <superproject name="platform/superproject" remote="aosp" revision="android-15.0.0_r36"/>
  <contactinfo bugurl="go/repo-bug" />
  <project path="build/make" name="platform/build" groups="pdk,sysui-studio" >
    <linkfile src="CleanSpec.mk" dest="build/CleanSpec.mk" />
    <linkfile src="core" dest="build/core" />
  </project>
  <project path="build/soong" name="platform/build/soong" groups="pdk,tradefed,sysui-studio" >
    <linkfile src="root.bp" dest="Android.bp" />
    <linkfile src="bootstrap.bash" dest="bootstrap.bash" />
  </project>
  <project path="art" name="platform/art" groups="pdk" />
  <project path="bionic" name="platform/bionic" groups="pdk" />
  <project path="device/google/atv" name="device/google/atv" groups="device,broadcom_pdk,generic_fs,pdk" />
  <project path="prebuilts/clang/host/linux-x86" name="platform/prebuilts/clang/host/linux-x86" groups="linux,pdk" clone-depth="1" />
  <project path="tools/repohooks" name="platform/tools/repohooks" groups="adt-infra,cts,developers,motodev,pdk,tools,tradefed" />
  <project path="external/kotlinc" name="platform/external/kotlinc" groups="pdk" clone-depth="1" />
</manifest>
`
	today, err := Parse([]byte(in))
	require.NoError(t, err)
	m, err := Parse([]byte(in))
	require.NoError(t, err)
	require.NoError(t, m.Effective())

	for _, p := range m.Project {
		require.Equal(t, xml.Name{Local: "project"}, p.XMLName)
	}
	require.Equal(t, today.Project, m.Project)
	require.Equal(t, today.Ordered, m.Ordered)
	want, err := xml.MarshalIndent(today, "", "  ")
	require.NoError(t, err)
	got, err := xml.MarshalIndent(m, "", "  ")
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestWarnings(t *testing.T) {
	files := map[string]string{
		"default.xml": `<manifest>
 <remote name="aosp" fetch=".." revision="main" annotation-free="x"></remote>
 <default remote="aosp" sync-j-max="8"></default>
 <submanifest name="sub" project="platform/sub"></submanifest>
 <project name="platform/a" path="a" rebase="false">
  <project name="nested" path="n"></project>
 </project>
 <include name="other.xml" future="1"></include>
</manifest>`,
		"other.xml": `<manifest>
 <project name="platform/b" sync-strategy="stateless" x-tool="1"></project>
 <project name="platform/c" x-tool="2"></project>
 <extend-project name="platform/b" unknown="1"></extend-project>
 <remove-project name="platform/a" when="always"></remove-project>
</manifest>`,
	}
	bytes := func(ctx context.Context, name string) ([]byte, error) {
		return []byte(files[name]), nil
	}

	m, err := Parse([]byte(files["default.xml"]))
	require.NoError(t, err)
	require.NoError(t, m.FlattenIncludes(context.Background(), bytes))
	require.NoError(t, m.Effective())
	require.Equal(t, []string{
		`<submanifest name="sub"> is not applied`,
		`<project name="platform/a">: nested project elements are not applied`,
		`<remote name="aosp">: attribute "annotation-free" is not applied`,
		`<default>: attribute "sync-j-max" is not applied`,
		`<project name="platform/a">: attribute "rebase" is not applied`,
		`<include name="other.xml">: attribute "future" is not applied`,
		`other.xml: <project name="platform/b">: attribute "sync-strategy" is not applied`,
		`other.xml: <project>: attribute "x-tool" on 2 elements is not applied`,
		`other.xml: <extend-project name="platform/b">: attribute "unknown" is not applied`,
		`other.xml: <remove-project name="platform/a">: attribute "when" is not applied`,
	}, m.Warnings())
	require.Len(t, m.Project, 2)
}

func TestWarnings_StandardManifestHasNone(t *testing.T) {
	in := `<manifest>
  <remote name="aosp" fetch=".." review="https://android-review.googlesource.com/" />
  <default revision="main" remote="aosp" sync-j="4" />
  <superproject name="platform/superproject" remote="aosp" revision="main"/>
  <contactinfo bugurl="go/repo-bug" />
  <project path="build/make" name="platform/build" groups="pdk" >
    <linkfile src="core" dest="build/core" />
  </project>
  <project path="prebuilts/clang" name="platform/prebuilts/clang" groups="pdk" clone-depth="1" />
</manifest>`
	m, err := Parse([]byte(in))
	require.NoError(t, err)
	require.Empty(t, m.Warnings())
}
