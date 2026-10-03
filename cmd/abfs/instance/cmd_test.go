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

package instance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/instance"
)

// setupMockInstances creates a temporary BaseDir with a "default" and "target" instance.
// This is shared across test files in the instance package.
func setupMockInstances(t *testing.T) (string, *instance.Instance) {
	t.Helper()
	baseDir := t.TempDir()

	// Create "default" instance directory structure
	defaultDir := filepath.Join(baseDir, "default", "cache")
	require.NoError(t, os.MkdirAll(defaultDir, 0755))

	// Create "target" instance directory structure
	targetDir := filepath.Join(baseDir, "target", "cache")
	require.NoError(t, os.MkdirAll(targetDir, 0755))

	// Create a mock active instance context representing "default"
	inst := &instance.Instance{
		Name:    "default",
		BaseDir: baseDir,
		Flavor:  instance.ABFSFlavor,
	}

	return baseDir, inst
}

// mockExecCommand replaces the package-level execCommand with a mock that returns
// a successful placeholder command (e.g., "true") and records the arguments it was called with.
// It automatically restores the original execCommand when the test completes.
func mockExecCommand(t *testing.T) *[][]string {
	t.Helper()
	var captured [][]string
	oldExecCommand := execCommand

	execCommand = func(name string, arg ...string) *exec.Cmd {
		captured = append(captured, append([]string{name}, arg...))
		return exec.Command("true")
	}

	t.Cleanup(func() {
		execCommand = oldExecCommand
	})

	return &captured
}

func TestRemoveCmd_Run(t *testing.T) {
	baseDir, inst := setupMockInstances(t)
	mockExecCommand(t)

	// Setup the command with the "default" active instance context
	instCmd := &instanceCmd{
		ConfigProvider: &cmd.ConfigProvider{Instance: inst},
	}
	rmCmd := NewRemoveCmd(instCmd)
	rmCmd.force = true // Bypass the stdin [yes/no] prompt

	// Run the removal on "target"
	err := rmCmd.run("target")
	require.NoError(t, err)

	// Verify "target" is removed
	assert.NoDirExists(t, filepath.Join(baseDir, "target"), "target directory should be removed")

	// Verify "default" remains untouched
	_, err = os.Stat(filepath.Join(baseDir, "default"))
	assert.NoError(t, err, "default directory should not be removed")
}

func TestRenameCmd_Run(t *testing.T) {
	baseDir, inst := setupMockInstances(t)
	mockExecCommand(t)

	// Setup the command with the "default" active instance context
	instCmd := &instanceCmd{
		ConfigProvider: &cmd.ConfigProvider{Instance: inst},
	}
	rnCmd := NewRenameCmd(instCmd)

	// Run the rename: "target" -> "new-target"
	err := rnCmd.run("target", "new-target")
	require.NoError(t, err)

	// Verify "target" is gone
	assert.NoDirExists(t, filepath.Join(baseDir, "target"), "old target directory should be gone")

	// Verify "new-target" exists
	_, err = os.Stat(filepath.Join(baseDir, "new-target"))
	assert.NoError(t, err, "new-target directory should exist")

	// Verify "default" remains untouched
	_, err = os.Stat(filepath.Join(baseDir, "default"))
	assert.NoError(t, err, "default directory should not be touched")
}
