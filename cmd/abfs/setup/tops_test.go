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

package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/instance"
)

// makeSoongTop creates the files Soong looks for at the top of an Android tree.
func makeSoongTop(t *testing.T, root, top string) {
	t.Helper()
	dir := filepath.Join(root, top)
	writeTestFile(t, filepath.Join(dir, soongRootFile), "// root.bp\n", 0644)
	binDirPath, err := binDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, binDirPath), 0755))
}

func writeProjectsList(t *testing.T, root string, paths ...string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, ".repo", "projects.list"), strings.Join(paths, "\n")+"\n", 0644)
}

func newTopsCmd(root string, config *instance.Config) *setupCmd {
	if config == nil {
		config = &instance.Config{}
	}
	return &setupCmd{
		ConfigProvider: &cmd.ConfigProvider{Config: config},
		mountPoint:     root,
	}
}

func TestCandidateTops(t *testing.T) {
	tests := []struct {
		name     string
		projects []string
		want     []string
	}{
		{
			name:     "standard layout",
			projects: []string{"build/make", "build/soong", "build/blueprint", "frameworks/base"},
			want:     []string{""},
		},
		{
			name:     "tree below the root",
			projects: []string{"android/build/make", "android/build/soong", "other/app", "tools/flash"},
			want:     []string{"android"},
		},
		{
			name:     "deeper tree",
			projects: []string{"src/aosp/build/soong", "src/aosp/frameworks/base"},
			want:     []string{"src/aosp"},
		},
		{
			name:     "two trees",
			projects: []string{"aosp15/build/soong", "aosp17/build/make", "aosp17/build/soong"},
			want:     []string{"aosp15", "aosp17"},
		},
		{
			name:     "nested tree",
			projects: []string{"build/soong", "vendor/sdk/build/soong"},
			want:     []string{"", "vendor/sdk"},
		},
		{
			name:     "build as one project",
			projects: []string{"android/build", "android/frameworks/base"},
			want:     []string{"android"},
		},
		{
			name:     "paths outside the tree are ignored",
			projects: []string{"../x/build/soong", "/abs/build/soong", "android/./build/soong/"},
			want:     []string{"android"},
		},
		{
			name:     "no Android tree",
			projects: []string{"other/app", "tools/flash"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, candidateTops(tt.projects))
		})
	}
}

func TestDetectTops(t *testing.T) {
	tests := []struct {
		name     string
		projects []string
		tops     []string // directories that pass Soong's test
		extra    func(t *testing.T, root string)
		want     []string
	}{
		{
			name:     "standard layout",
			projects: []string{"build/make", "build/soong", "prebuilts/build-tools"},
			tops:     []string{""},
			want:     []string{""},
		},
		{
			name:     "tree below the root",
			projects: []string{"android/build/make", "android/build/soong", "other/app"},
			tops:     []string{"android"},
			want:     []string{"android"},
		},
		{
			name:     "deeper tree",
			projects: []string{"src/aosp/build/soong"},
			tops:     []string{"src/aosp"},
			want:     []string{"src/aosp"},
		},
		{
			name:     "two trees",
			projects: []string{"a/build/soong", "b/build/soong"},
			tops:     []string{"a", "b"},
			want:     []string{"a", "b"},
		},
		{
			name:     "nested tree",
			projects: []string{"build/soong", "vendor/sdk/build/soong"},
			tops:     []string{"", "vendor/sdk"},
			want:     []string{"", "vendor/sdk"},
		},
		{
			name:     "build as one project",
			projects: []string{"android/build"},
			tops:     []string{"android"},
			want:     []string{"android"},
		},
		{
			name:     "build/soong without root.bp is not a top",
			projects: []string{"android/build/soong"},
			extra: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "android/build/soong/Android.bp"), "", 0644)
				require.NoError(t, os.MkdirAll(filepath.Join(root, "android/prebuilts/build-tools/linux-x86/bin"), 0755))
			},
		},
		{
			name:     "root.bp without prebuilt tools is not a top",
			projects: []string{"android/build/soong"},
			extra: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "android", soongRootFile), "", 0644)
			},
		},
		{
			name: "a top that is not in the project list is not found",
			tops: []string{"android"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.projects != nil {
				writeProjectsList(t, root, tt.projects...)
			}
			for _, top := range tt.tops {
				makeSoongTop(t, root, top)
			}
			if tt.extra != nil {
				tt.extra(t, root)
			}
			got, err := DetectTops(root)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDetectTops_PinnedManifest(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".repo/abfs-manifests/pinned.xml"), `<manifest>
  <remote name="origin" fetch=".."/>
  <default remote="origin" revision="main"/>
  <project name="platform/build/soong" path="android/build/soong"/>
  <project name="platform/build" path="android/build/make"/>
  <project name="other/app" path="other"/>
</manifest>
`, 0644)
	makeSoongTop(t, root, "android")

	got, err := DetectTops(root)
	require.NoError(t, err)
	require.Equal(t, []string{"android"}, got)
}

func TestFindTops(t *testing.T) {
	t.Run("detection", func(t *testing.T) {
		root := t.TempDir()
		writeProjectsList(t, root, "android/build/soong")
		makeSoongTop(t, root, "android")
		tops, source, err := newTopsCmd(root, nil).findTops(root)
		require.NoError(t, err)
		require.Equal(t, []string{"android"}, tops)
		require.Equal(t, "detection", source)
	})

	t.Run("fallback to the mount root", func(t *testing.T) {
		root := t.TempDir()
		var warnings bytes.Buffer
		c := newTopsCmd(root, nil)
		c.errOut = &warnings
		tops, source, err := c.findTops(root)
		require.NoError(t, err)
		require.Equal(t, []string{""}, tops)
		require.Contains(t, source, "fallback")
		require.Empty(t, warnings.String(), "a tree without Android projects is no surprise")
	})

	t.Run("fallback names the candidates that failed", func(t *testing.T) {
		root := t.TempDir()
		writeProjectsList(t, root, "android/build/soong", "android/prebuilts/build-tools", "a2/build/soong")
		writeTestFile(t, filepath.Join(root, "android", soongRootFile), "", 0644)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "a2/prebuilts/build-tools/linux-x86/bin"), 0755))
		var warnings bytes.Buffer
		c := newTopsCmd(root, nil)
		c.errOut = &warnings
		tops, _, err := c.findTops(root)
		require.NoError(t, err)
		require.Equal(t, []string{""}, tops)
		require.Equal(t, "Warning: no Soong top found in "+root+", so setup uses the mount root; candidates: "+
			"a2 (build/soong/root.bp missing), android (prebuilts/build-tools/linux-x86/bin/ missing)\n", warnings.String())
	})

	t.Run("--chroot need not exist yet", func(t *testing.T) {
		root := t.TempDir()
		c := newTopsCmd(root, nil)
		c.Chroot = "chroot"
		tops, source, err := c.findTops(root)
		require.NoError(t, err)
		require.Equal(t, []string{"chroot"}, tops)
		require.Equal(t, "--chroot", source)
		c.Chroot = "../x"
		_, _, err = c.findTops(root)
		require.ErrorContains(t, err, "outside the mount point")
	})

	t.Run("repo checkout below the mount root", func(t *testing.T) {
		root := t.TempDir()
		repoRoot := filepath.Join(root, "work", "aosp")
		require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".repo", "repo"), 0755))
		writeProjectsList(t, repoRoot, "build/soong")
		makeSoongTop(t, repoRoot, "")
		tops, _, err := newTopsCmd(root, nil).findTops(filepath.Join(repoRoot, "build"))
		require.NoError(t, err)
		require.Equal(t, []string{"work/aosp"}, tops)
	})
}

func TestListTops(t *testing.T) {
	root := t.TempDir()
	writeProjectsList(t, root, "android/build/soong")
	makeSoongTop(t, root, "android")
	writeTestFile(t, filepath.Join(root, "android", vendorSetupRelativePath), vendorSetupContent, 0644)

	c := newTopsCmd(root, nil)
	tops, source, err := c.findTops(root)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, c.listTops(&out, tops, source))
	got := out.String()
	require.Contains(t, got, "Soong tops in "+root+" (detection):\nandroid\n")
	require.Contains(t, got, "vendor/abfs/vendorsetup.sh   present")
	require.Contains(t, got, ".ninjago.conf                missing")
	require.Contains(t, got, ".abfs.sock                   not served")
	require.NotContains(t, got, "not a Soong top")
}
