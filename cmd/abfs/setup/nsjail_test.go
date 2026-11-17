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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Excerpts of build/soong/ui/build/sandbox_linux.go before and after Soong reads the cache
// directory from .repo/mount-details.
const (
	sandboxWithPlaceholder = `	if c.config.UseABFS() {
		sandboxArgs = append(sandboxArgs, "-B", "{ABFS_DIR}")
	}
`
	sandboxFixed = `	if c.config.UseABFS() {
		cacheDir, err := abfsCacheFromMount()
		if err != nil {
			c.ctx.Fatalln(err)
		}
		sandboxArgs = append(sandboxArgs, "-B", cacheDir)
	}
`
	realNSJail = "\x7fELF real nsjail"
)

func writeTestFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// newTestTree returns the root of a tree with an nsjail prebuilt and, unless sandbox is empty,
// Soong's sandbox_linux.go.
func newTestTree(t *testing.T, sandbox string) string {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, nsjailRelativePath), realNSJail, 0755)
	if sandbox != "" {
		writeTestFile(t, filepath.Join(root, soongSandboxRelativePath), sandbox, 0644)
	}
	return root
}

func TestSoongPassesABFSDir(t *testing.T) {
	tests := []struct {
		name    string
		sandbox string
		want    bool
	}{
		{name: "placeholder", sandbox: sandboxWithPlaceholder, want: true},
		{name: "fixed", sandbox: sandboxFixed, want: false},
		{name: "no soong", sandbox: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := soongPassesABFSDir(newTestTree(t, tt.sandbox))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWrapNSJail(t *testing.T) {
	root := newTestTree(t, sandboxWithPlaceholder)
	nsjail := filepath.Join(root, nsjailRelativePath)

	// A second run must keep the original in nsjail.real, not the wrapper.
	for i := 0; i < 2; i++ {
		if err := wrapNSJail(root, "/home/u/.abfs/default/cache"); err != nil {
			t.Fatal(err)
		}
	}
	if got := readTestFile(t, nsjail+".real"); got != realNSJail {
		t.Errorf("nsjail.real = %q, want the original", got)
	}
	wrapper := readTestFile(t, nsjail)
	if !strings.Contains(wrapper, nsjailWrapperHeader) || !strings.Contains(wrapper, "/home/u/.abfs/default/cache") {
		t.Errorf("nsjail is not the wrapper for the cache dir:\n%s", wrapper)
	}
	exclude := readTestFile(t, filepath.Join(root, buildToolsRelativeDir, ".git", "info", "exclude"))
	if !strings.Contains(exclude, "/linux-x86/bin/nsjail.real\n") {
		t.Errorf("git exclude does not list nsjail.real:\n%s", exclude)
	}

	// A tree that carries Soong's fix gets the original back.
	writeTestFile(t, filepath.Join(root, soongSandboxRelativePath), sandboxFixed, 0644)
	if err := wrapNSJail(root, "/home/u/.abfs/default/cache"); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, nsjail); got != realNSJail {
		t.Errorf("nsjail = %q, want the original", got)
	}
	if _, err := os.Stat(nsjail + ".real"); !os.IsNotExist(err) {
		t.Errorf("nsjail.real still exists: %v", err)
	}
}

func TestWrapNSJail_NoNSJail(t *testing.T) {
	root := newTestTree(t, sandboxWithPlaceholder)
	if err := os.Remove(filepath.Join(root, nsjailRelativePath)); err != nil {
		t.Fatal(err)
	}
	if err := wrapNSJail(root, "/cache"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, nsjailRelativePath)); !os.IsNotExist(err) {
		t.Errorf("nsjail was created: %v", err)
	}
}

func TestKeepNSJailWrapper(t *testing.T) {
	const newNSJail = "\x7fELF new nsjail"
	tests := []struct {
		name        string
		newSandbox  string
		syncErr     error
		wantWrapped bool
	}{
		{name: "still needed", newSandbox: sandboxWithPlaceholder, wantWrapped: true},
		// repo falls back to its own sync, which must not meet the wrapper.
		{name: "sync fails", newSandbox: sandboxWithPlaceholder, syncErr: errors.New("sync failed"), wantWrapped: false},
		{name: "fixed by the sync", newSandbox: sandboxFixed, wantWrapped: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestTree(t, sandboxWithPlaceholder)
			nsjail := filepath.Join(root, nsjailRelativePath)
			if err := wrapNSJail(root, "/cache"); err != nil {
				t.Fatal(err)
			}

			err := KeepNSJailWrapper(root, func() error {
				// The checkout must see the original, then it brings a new nsjail.
				if got := readTestFile(t, nsjail); got != realNSJail {
					t.Errorf("nsjail during sync = %q, want the original", got)
				}
				writeTestFile(t, nsjail, newNSJail, 0755)
				writeTestFile(t, filepath.Join(root, soongSandboxRelativePath), tt.newSandbox, 0644)
				return tt.syncErr
			})
			if !errors.Is(err, tt.syncErr) {
				t.Fatalf("got error %v, want %v", err, tt.syncErr)
			}

			want := newNSJail
			if tt.wantWrapped {
				if got := readTestFile(t, nsjail+".real"); got != newNSJail {
					t.Errorf("nsjail.real = %q, want the synced nsjail", got)
				}
				want = readTestFile(t, nsjail)
				if !strings.Contains(want, nsjailWrapperHeader) {
					t.Errorf("nsjail is not the wrapper:\n%s", want)
				}
			}
			if got := readTestFile(t, nsjail); got != want {
				t.Errorf("nsjail = %q, want %q", got, want)
			}
		})
	}
}

func TestWrapNSJail_Interrupted(t *testing.T) {
	tests := []struct {
		name        string
		sandbox     string
		wantWrapped bool
	}{
		{name: "still needed", sandbox: sandboxWithPlaceholder, wantWrapped: true},
		{name: "fixed", sandbox: sandboxFixed, wantWrapped: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A run that stopped after moving the original aside.
			root := newTestTree(t, tt.sandbox)
			nsjail := filepath.Join(root, nsjailRelativePath)
			if err := os.Rename(nsjail, nsjail+".real"); err != nil {
				t.Fatal(err)
			}
			if err := wrapNSJail(root, "/cache"); err != nil {
				t.Fatal(err)
			}
			got := readTestFile(t, nsjail)
			if tt.wantWrapped {
				if !strings.Contains(got, nsjailWrapperHeader) {
					t.Errorf("nsjail is not the wrapper:\n%s", got)
				}
				if orig := readTestFile(t, nsjail+".real"); orig != realNSJail {
					t.Errorf("nsjail.real = %q, want the original", orig)
				}
			} else if got != realNSJail {
				t.Errorf("nsjail = %q, want the original", got)
			}
		})
	}
}

func TestWrapNSJail_RealMissing(t *testing.T) {
	root := newTestTree(t, sandboxWithPlaceholder)
	nsjail := filepath.Join(root, nsjailRelativePath)
	if err := wrapNSJail(root, "/cache"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(nsjail + ".real"); err != nil {
		t.Fatal(err)
	}

	if err := wrapNSJail(root, "/cache"); err == nil || !strings.Contains(err.Error(), "checkout -- linux-x86/bin/nsjail") {
		t.Errorf("wrapNSJail: got %v, want an error that names the restore command", err)
	}
	ran := false
	if err := KeepNSJailWrapper(root, func() error { ran = true; return nil }); err == nil {
		t.Error("KeepNSJailWrapper: got no error")
	}
	if ran {
		t.Error("sync ran with a broken wrapper")
	}
}

func TestWrapNSJail_OtherScript(t *testing.T) {
	root := newTestTree(t, sandboxWithPlaceholder)
	nsjail := filepath.Join(root, nsjailRelativePath)
	const other = "#!/bin/bash\nexec \"${0%/*}/nsjail.real\" \"$@\"\n"
	writeTestFile(t, nsjail, other, 0755)

	if err := wrapNSJail(root, "/cache"); err == nil {
		t.Error("got no error")
	}
	if got := readTestFile(t, nsjail); got != other {
		t.Errorf("nsjail = %q, want it unchanged", got)
	}
	if _, err := os.Stat(nsjail + ".real"); !os.IsNotExist(err) {
		t.Errorf("nsjail.real exists: %v", err)
	}
}

func TestKeepNSJailWrapper_NotWrapped(t *testing.T) {
	root := newTestTree(t, sandboxFixed)
	ran := false
	if err := KeepNSJailWrapper(root, func() error { ran = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("sync did not run")
	}
	if got := readTestFile(t, filepath.Join(root, nsjailRelativePath)); got != realNSJail {
		t.Errorf("nsjail = %q, want the original", got)
	}
}

func TestNSJailWrapperContent(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "nsjail")
	writeTestFile(t, wrapper+".real", "#!"+bash+"\nfor a in \"$@\"; do echo \"[$a]\"; done\n", 0755)
	cacheDir := `/home/a b/it's & $x/cache`
	writeTestFile(t, wrapper, string(nsjailWrapper(cacheDir)), 0755)

	// Soong runs nsjail with a minimal PATH, so the wrapper must not need one.
	cmd := exec.Command(wrapper, "-B", "{ABFS_DIR}", "-B", "/src:/src", "--", "x{ABFS_DIR}y")
	cmd.Env = []string{"PATH=/nonexistent"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper failed: %v\n%s", err, out)
	}
	want := "[-B]\n[" + cacheDir + "]\n[-B]\n[/src:/src]\n[--]\n[x" + cacheDir + "y]\n"
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}
