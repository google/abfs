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

package editor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsVim(t *testing.T) {
	require := require.New(t)
	require.True(IsVim("vi"))
	require.True(IsVim("/usr/bin/vim"))
	require.True(IsVim("nvim"))
	require.True(IsVim("vim.basic"))
	require.True(IsVim("gvim"))
	require.False(IsVim("nano"))
	require.False(IsVim("code"))
}

func TestResolve(t *testing.T) {
	require := require.New(t)

	t.Setenv("VISUAL", "nvim -u NONE")
	t.Setenv("EDITOR", "vim")
	require.Equal([]string{"nvim", "-u", "NONE"}, Resolve())

	t.Setenv("VISUAL", "")
	require.Equal([]string{"vim"}, Resolve())
}

func TestEditWithValidation_ReopenAndFix(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "test.yaml")
	require.NoError(os.WriteFile(target, []byte("initial: true\n"), 0600))

	oldExec := ExecCommand
	t.Cleanup(func() {
		ExecCommand = oldExec
	})
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "mock-editor")

	calls := 0
	var secondSawErrorBanner bool

	ExecCommand = func(name string, arg ...string) *exec.Cmd {
		if name == "mock-editor" {
			calls++
			filePath := arg[len(arg)-1]
			if calls == 1 {
				_ = os.WriteFile(filePath, []byte("bad-key: true\n"), 0600)
				return exec.Command("true")
			}
			data, _ := os.ReadFile(filePath)
			secondSawErrorBanner = strings.Contains(string(data), "VALIDATION ERROR")
			fixed := strings.ReplaceAll(string(data), "bad-key:", "good-key:")
			_ = os.WriteFile(filePath, []byte(fixed), 0600)
			return exec.Command("true")
		}
		return oldExec(name, arg...)
	}

	err := EditWithValidation(EditOptions{
		TargetFile:    target,
		FileExtension: ".yaml",
		Validate: func(editedPath string) error {
			data, err := os.ReadFile(editedPath)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), "bad-key") {
				return errors.New("unknown field bad-key")
			}
			return nil
		},
	})
	require.NoError(err)
	require.Equal(2, calls)
	require.True(secondSawErrorBanner)

	finalData, err := os.ReadFile(target)
	require.NoError(err)
	require.Equal("good-key: true\n", string(finalData))
}
