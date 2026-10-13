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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTopmostMissingDir(t *testing.T) {
	root := t.TempDir()
	if got := topmostMissingDir(root, "vendor/abfs"); got != "vendor" {
		t.Errorf("no dirs exist: got %q, want %q", got, "vendor")
	}

	if err := os.Mkdir(filepath.Join(root, "vendor"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := topmostMissingDir(root, "vendor/abfs"); got != "vendor/abfs" {
		t.Errorf("vendor exists: got %q, want %q", got, "vendor/abfs")
	}

	if err := os.Mkdir(filepath.Join(root, "vendor", "abfs"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := topmostMissingDir(root, "vendor/abfs"); got != "" {
		t.Errorf("all dirs exist: got %q, want %q", got, "")
	}
}

func TestVendorSetupContent(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	script := filepath.Join(t.TempDir(), "vendorsetup.sh")
	if err := os.WriteFile(script, []byte(vendorSetupContent), 0644); err != nil {
		t.Fatal(err)
	}

	vars := []string{"SOONG_NINJA", "BUILD_HOSTNAME", "BUILD_USERNAME", "BUILD_DATETIME", "BUILD_NUMBER"}
	source := func(env ...string) string {
		t.Helper()
		cmd := exec.Command(bash, "-c", `set -u; . "$0"; for v in "$@"; do echo "$v=${!v:-}"; done`, script)
		cmd.Args = append(cmd.Args, vars...)
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sourcing vendorsetup.sh failed: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}

	tests := []struct {
		name string
		env  []string
		want string
	}{
		{
			name: "defaults",
			want: "SOONG_NINJA=ninjago\nBUILD_HOSTNAME=abfs-builder\nBUILD_USERNAME=abfs-builder\nBUILD_DATETIME=1\nBUILD_NUMBER=eng",
		},
		{
			name: "keeps existing values",
			env:  []string{"SOONG_NINJA=ninja", "BUILD_USERNAME=me"},
			want: "SOONG_NINJA=ninja\nBUILD_HOSTNAME=abfs-builder\nBUILD_USERNAME=me\nBUILD_DATETIME=1\nBUILD_NUMBER=eng",
		},
		{
			name: "opt out",
			env:  []string{"SKIP_ABFS_COERCE=1"},
			want: "SOONG_NINJA=\nBUILD_HOSTNAME=\nBUILD_USERNAME=\nBUILD_DATETIME=\nBUILD_NUMBER=",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := source(tc.env...); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
