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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/ninjago"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
)

// fakeCASFS records the exclusion flags that setup sets on a tree that is not a casfs mount.
type fakeCASFS struct {
	bindcas.CASFS
	mu       sync.Mutex
	excluded map[string]bool
	sets     map[string]int
}

func (f *fakeCASFS) SetExcluded(path string, val bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.excluded[path] = val
	f.sets[path]++
	return nil
}

func (f *fakeCASFS) HashSize(path string) (entities.Hash, int64, error) {
	return entities.EmptyFileHash, 0, nil
}

func (f *fakeCASFS) SetHashSize(path string, h entities.Hash, s int64) error {
	return nil
}

func (f *fakeCASFS) Excluded(path string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.excluded[path], nil
}

// excludedPaths returns the excluded paths relative to root.
func (f *fakeCASFS) excludedPaths(t *testing.T, root string) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var paths []string
	for p, val := range f.excluded {
		if val {
			rel, err := filepath.Rel(root, p)
			require.NoError(t, err)
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	return paths
}

func useFakeCASFS(t *testing.T) *fakeCASFS {
	t.Helper()
	f := &fakeCASFS{excluded: map[string]bool{}, sets: map[string]int{}}
	bindcas.SetGlobalCASFS(f)
	t.Cleanup(func() { bindcas.SetGlobalCASFS(bindcas.NewExternalCASFS("")) })
	return f
}

// fakeChrootClient records the chroot requests that setup sends to the daemon and, like the
// daemon, creates the chroot directory.
type fakeChrootClient struct {
	mountPoint string
	chroots    []string
}

func (f *fakeChrootClient) Chroot(chroot string, overlays, views []string) error {
	f.chroots = append(f.chroots, chroot)
	return os.MkdirAll(filepath.Join(f.mountPoint, chroot), 0755)
}

// installedBinaries puts fake -no-cgo binaries on PATH, as `make install-client` would.
func installedBinaries(t *testing.T, skip ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, b := range setupBinaries {
		if b == "ckati-wrap" || strings.Contains(strings.Join(skip, " "), b) {
			continue
		}
		writeTestFile(t, filepath.Join(dir, b+"-no-cgo"), "#!/bin/true\n# "+b+"\n", 0755)
	}
	t.Setenv("PATH", dir)
}

func newSetupForTest(t *testing.T, root string) (*setupCmd, *fakeChrootClient) {
	t.Helper()
	client := &fakeChrootClient{mountPoint: root}
	c := newTopsCmd(root, &instance.Config{CacheDir: "/home/user/.abfs/cache"})
	c.DisableCGO = true
	c.quiet = true
	c.abfs = client
	return c, client
}

// treeFiles lists the files and links under root, relative to it, without the files the test
// tree started with.
func treeFiles(t *testing.T, root string, before map[string]bool) []string {
	t.Helper()
	var files []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if !before[rel] {
			files = append(files, rel)
		}
		return nil
	}))
	sort.Strings(files)
	return files
}

func snapshotFiles(t *testing.T, root string) map[string]bool {
	t.Helper()
	before := map[string]bool{}
	for _, f := range treeFiles(t, root, nil) {
		before[f] = true
	}
	return before
}

// generatedFiles lists the files setup writes in a top, relative to the mount point.
func generatedFiles(top string) []string {
	files := []string{
		".ninjago.conf",
		"prebuilts/build-tools/.git/info/exclude",
		"vendor/abfs/vendorsetup.sh",
	}
	for _, b := range setupBinaries {
		files = append(files, "prebuilts/build-tools/linux-x86/bin/"+b)
	}
	if top != "" {
		files = append(files, ".repo/mount-details")
	}
	for i, f := range files {
		files[i] = filepath.Join(top, f)
	}
	return files
}

func readConf(t *testing.T, dir string) *ninjago.Configuration {
	t.Helper()
	conf, err := ninjago.ReadConfig(filepath.Join(dir, ninjago.ConfigFile))
	require.NoError(t, err)
	return conf
}

func TestSetup_StandardLayout(t *testing.T) {
	cas := useFakeCASFS(t)
	installedBinaries(t)
	root := t.TempDir()
	writeProjectsList(t, root, "build/make", "build/soong", "prebuilts/build-tools")
	makeSoongTop(t, root, "")
	before := snapshotFiles(t, root)

	c, client := newSetupForTest(t, root)
	require.NoError(t, c.run(root))

	want := generatedFiles("")
	sort.Strings(want)
	require.Equal(t, want, treeFiles(t, root, before))
	conf := readConf(t, root)
	require.Empty(t, conf.Chroot)
	require.Equal(t, "/home/user/.abfs/cache", conf.CacheDir)
	require.Empty(t, client.chroots, "no chroot request at the mount root")
	require.Equal(t, []string{".ninjago.conf", "vendor"}, cas.excludedPaths(t, root))
	require.Equal(t, "#!/bin/true\n# ninjago\n", readTestFile(t, filepath.Join(root, "prebuilts/build-tools/linux-x86/bin/ninjago")))
}

func TestSetup_TreeBelowTheRoot(t *testing.T) {
	cas := useFakeCASFS(t)
	installedBinaries(t)
	root := t.TempDir()
	writeProjectsList(t, root, "android/build/make", "android/build/soong", "other/app", "tools/flash")
	makeSoongTop(t, root, "android")
	before := snapshotFiles(t, root)

	c, client := newSetupForTest(t, root)
	require.NoError(t, c.run(root))

	want := generatedFiles("android")
	sort.Strings(want)
	require.Equal(t, want, treeFiles(t, root, before), "setup writes into the top only")
	require.Equal(t, "android", readConf(t, filepath.Join(root, "android")).Chroot)
	require.Equal(t, []string{"android"}, client.chroots)
	link, err := os.Readlink(filepath.Join(root, "android/.repo/mount-details"))
	require.NoError(t, err)
	require.Equal(t, "../../.abfs.mount-details", link)
	require.Equal(t, []string{"android/.ninjago.conf", "android/vendor"}, cas.excludedPaths(t, root), "the top itself is not excluded")
}

func TestSetup_SeveralTops(t *testing.T) {
	useFakeCASFS(t)
	installedBinaries(t)
	root := t.TempDir()
	writeProjectsList(t, root, "build/soong", "src/aosp/build/soong", "src/aosp/vendor/sdk/build/soong")
	makeSoongTop(t, root, "")
	makeSoongTop(t, root, "src/aosp")
	makeSoongTop(t, root, "src/aosp/vendor/sdk")
	before := snapshotFiles(t, root)

	c, client := newSetupForTest(t, root)
	require.NoError(t, c.run(root))

	var want []string
	for _, top := range []string{"", "src/aosp", "src/aosp/vendor/sdk"} {
		want = append(want, generatedFiles(top)...)
	}
	sort.Strings(want)
	require.Equal(t, want, treeFiles(t, root, before))
	require.Equal(t, []string{"src/aosp", "src/aosp/vendor/sdk"}, client.chroots)
	require.Equal(t, "src/aosp/vendor/sdk", readConf(t, filepath.Join(root, "src/aosp/vendor/sdk")).Chroot)
	link, err := os.Readlink(filepath.Join(root, "src/aosp/vendor/sdk/.repo/mount-details"))
	require.NoError(t, err)
	require.Equal(t, "../../../../../.abfs.mount-details", link)
}

func TestSetup_SecondRunRewrites(t *testing.T) {
	cas := useFakeCASFS(t)
	installedBinaries(t)
	root := t.TempDir()
	writeProjectsList(t, root, "android/build/soong")
	makeSoongTop(t, root, "android")
	before := snapshotFiles(t, root)

	c, _ := newSetupForTest(t, root)
	require.NoError(t, c.run(root))
	first := treeFiles(t, root, before)

	top := filepath.Join(root, "android")
	require.NoError(t, os.Remove(filepath.Join(top, vendorSetupRelativePath)))
	writeTestFile(t, filepath.Join(top, ninjago.ConfigFile), `{"CacheDir": "/other/machine"}`, 0644)

	require.NoError(t, c.run(root))
	require.Equal(t, first, treeFiles(t, root, before))
	require.Equal(t, vendorSetupContent, readTestFile(t, filepath.Join(top, vendorSetupRelativePath)))
	require.Equal(t, "/home/user/.abfs/cache", readConf(t, top).CacheDir)
	require.Equal(t, 2, cas.sets[filepath.Join(top, ninjago.ConfigFile)], "the flag is set again after each write")
	// The first run excludes vendor/, which it created; the second one the file it rewrote.
	require.True(t, cas.excluded[filepath.Join(top, vendorSetupRelativePath)])
}

func TestSetup_MissingBinary(t *testing.T) {
	useFakeCASFS(t)
	installedBinaries(t, "abfsbox")
	root := t.TempDir()
	makeSoongTop(t, root, "")

	c, _ := newSetupForTest(t, root)
	err := c.run(root)
	require.ErrorContains(t, err, "cannot find abfsbox-no-cgo on PATH")
	require.ErrorContains(t, err, "make install-client")
	require.NoFileExists(t, filepath.Join(root, "prebuilts/build-tools/linux-x86/bin/abfsbox"))
}

func TestSetup_MigratesChrootTree(t *testing.T) {
	installedBinaries(t)
	root := t.TempDir()
	writeProjectsList(t, root, "android/build/soong")
	makeSoongTop(t, root, "android")
	// What `abfs setup --chroot android` left before setup knew about tops.
	top := filepath.Join(root, "android")
	writeTestFile(t, filepath.Join(top, ".abfs.mount-details"), "{}", 0644)
	require.NoError(t, os.MkdirAll(filepath.Join(top, ".repo"), 0755))
	require.NoError(t, os.Symlink("../.abfs.mount-details", filepath.Join(top, ".repo/mount-details")))

	cas := useFakeCASFS(t)
	cas.excluded[top] = true

	c, _ := newSetupForTest(t, root)
	require.NoError(t, c.run(root))

	require.False(t, cas.excluded[top], "the top is in the workspace snapshot again")
	require.NoFileExists(t, filepath.Join(top, ".abfs.mount-details"))
	link, err := os.Readlink(filepath.Join(top, ".repo/mount-details"))
	require.NoError(t, err)
	require.Equal(t, "../../.abfs.mount-details", link)
}

func TestSetup_NewChroot(t *testing.T) {
	cas := useFakeCASFS(t)
	installedBinaries(t)
	root := t.TempDir()
	makeSoongTop(t, root, "")

	// abfs nsjail sets up a chroot that does not exist yet on a fresh classic mount.
	c, client := newSetupForTest(t, root)
	c.Chroot = "chroot"
	require.NoError(t, c.run(root))
	require.Equal(t, []string{"chroot"}, client.chroots)
	dir := filepath.Join(root, "chroot")
	require.Equal(t, "chroot", readConf(t, dir).Chroot)

	// abfs nsjail binds only the chroot, so the mount details are a copy inside it, and the
	// scratch checkout stays out of the workspace snapshot.
	require.FileExists(t, filepath.Join(dir, ".abfs.mount-details"))
	link, err := os.Readlink(filepath.Join(dir, ".repo/mount-details"))
	require.NoError(t, err)
	require.Equal(t, "../.abfs.mount-details", link)
	require.True(t, cas.excluded[dir])
}

func TestSetup_ChrootAfterDetection(t *testing.T) {
	useFakeCASFS(t)
	installedBinaries(t)
	root := t.TempDir()
	writeProjectsList(t, root, "android/build/soong")
	makeSoongTop(t, root, "android")
	link := filepath.Join(root, "android/.repo/mount-details")

	c, _ := newSetupForTest(t, root)
	require.NoError(t, c.run(root))
	target, err := os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, "../../.abfs.mount-details", target)

	// abfs nsjail binds only the chroot, where ../../ leaves it.
	c.Chroot = "android"
	require.NoError(t, c.run(root))
	target, err = os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, "../.abfs.mount-details", target)
}

func TestSetup_ProjectTopStaysExcluded(t *testing.T) {
	cas := useFakeCASFS(t)
	installedBinaries(t)
	root := t.TempDir()
	makeSoongTop(t, root, "android")
	top := filepath.Join(root, "android")
	require.NoError(t, os.Mkdir(filepath.Join(top, ".git"), 0755))
	cas.excluded[top] = true

	c, _ := newSetupForTest(t, root)
	c.Tops = []string{"android"}
	require.NoError(t, c.run(root))
	require.True(t, cas.excluded[top], "a project directory is kept as a git repository")
}

func TestSetup_RunningBuild(t *testing.T) {
	useFakeCASFS(t)
	installedBinaries(t)
	root := t.TempDir()
	writeProjectsList(t, root, "android/build/soong")
	makeSoongTop(t, root, "android")
	top := filepath.Join(root, "android")
	ninjagoPath := filepath.Join(top, "prebuilts/build-tools/linux-x86/bin/ninjago")
	writeTestFile(t, ninjagoPath, "running ninjago", 0755)
	writeTestFile(t, filepath.Join(top, "out/.build_start"), "{}", 0644)

	c, _ := newSetupForTest(t, root)
	require.NoError(t, c.run(root))
	require.Equal(t, "running ninjago", readTestFile(t, ninjagoPath), "binaries are not replaced during a build")
	require.Equal(t, "android", readConf(t, top).Chroot, "the other files are written")
	require.FileExists(t, filepath.Join(top, vendorSetupRelativePath))

	writeTestFile(t, filepath.Join(top, "out/.build_info"), "{}", 0644)
	require.NoError(t, c.run(root))
	require.Equal(t, "#!/bin/true\n# ninjago\n", readTestFile(t, ninjagoPath), "binaries are replaced after the build")
}

func TestKeepNSJailWrapper_UnreadableProjects(t *testing.T) {
	root := newTestTree(t, sandboxWithPlaceholder)
	writeTestFile(t, filepath.Join(root, ".repo/abfs-manifests/pinned.xml"), "<manifest><project", 0644)
	require.NoError(t, wrapNSJail(root, "/cache"))

	synced := false
	require.NoError(t, KeepNSJailWrapper(root, func() error {
		synced = true
		require.Equal(t, realNSJail, readTestFile(t, filepath.Join(root, nsjailRelativePath)), "the root is still unwrapped")
		return nil
	}))
	require.True(t, synced, "a bad project list does not stop the sync")
	require.Equal(t, string(nsjailWrapper("/cache")), readTestFile(t, filepath.Join(root, nsjailRelativePath)))
}

func TestKeepNSJailWrapper_EveryTop(t *testing.T) {
	root := t.TempDir()
	writeProjectsList(t, root, "android/build/soong")
	makeSoongTop(t, root, "android")
	top := filepath.Join(root, "android")
	writeTestFile(t, filepath.Join(top, nsjailRelativePath), realNSJail, 0755)
	writeTestFile(t, filepath.Join(top, soongSandboxRelativePath), sandboxWithPlaceholder, 0644)
	require.NoError(t, wrapNSJail(top, "/cache"))

	synced := false
	require.NoError(t, KeepNSJailWrapper(root, func() error {
		synced = true
		require.Equal(t, realNSJail, readTestFile(t, filepath.Join(top, nsjailRelativePath)), "sync runs with the original nsjail")
		return nil
	}))
	require.True(t, synced)
	require.Equal(t, string(nsjailWrapper("/cache")), readTestFile(t, filepath.Join(top, nsjailRelativePath)))
}
