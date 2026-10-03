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

package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/editor"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/instance"
)

func setupTestConfigCmd(t *testing.T) (*configCmd, string) {
	t.Helper()
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config")
	inst := &instance.Instance{
		Name:          "test",
		Dir:           dir,
		ConfigFile:    configFile,
		DefaultConfig: instance.DefaultConfig(dir),
		RemoteConfig:  &instance.ConfigOptional{},
		LocalConfig:   &instance.ConfigOptional{},
		FlagsConfig:   &instance.ConfigOptional{},
	}
	c := NewCmd(root.NewRootCommand())
	c.ConfigProvider = &cmd.ConfigProvider{
		Instance: inst,
		Config:   inst.DefaultConfig.Clone(),
	}
	return c, configFile
}

func mockEditor(t *testing.T, fn func(editedFilePath string) error) {
	t.Helper()
	oldExec := editor.ExecCommand
	t.Cleanup(func() {
		editor.ExecCommand = oldExec
	})
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "mock-editor")

	editor.ExecCommand = func(name string, arg ...string) *exec.Cmd {
		if name == "mock-editor" && len(arg) > 0 {
			filePath := arg[len(arg)-1]
			if err := fn(filePath); err != nil {
				return exec.Command("false")
			}
			return exec.Command("true")
		}
		return oldExec(name, arg...)
	}
}

func TestEditConfig_Success(t *testing.T) {
	require := require.New(t)
	c, configFile := setupTestConfigCmd(t)

	initialContent := "# existing comment\ncache-dir: /old/cache\n"
	require.NoError(os.WriteFile(configFile, []byte(initialContent), 0600))

	var editorSawPath string
	var editorSawContent string
	newContent := "# existing comment\ncache-dir: /new/cache\nfs: kmod\n"

	mockEditor(t, func(editedFilePath string) error {
		editorSawPath = editedFilePath
		data, err := os.ReadFile(editedFilePath)
		if err != nil {
			return err
		}
		editorSawContent = string(data)
		return os.WriteFile(editedFilePath, []byte(newContent), 0600)
	})

	c.edit = true
	fl := pflag.NewFlagSet("config", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	require.NoError(fl.Parse([]string{"-e"}))

	require.NoError(c.Exec(context.Background(), fl))

	// Editor must edit a temporary copy, not the original configFile directly
	require.NotEqual(configFile, editorSawPath)
	require.Equal(initialContent, editorSawContent)

	// Final config file should have the new content
	finalData, err := os.ReadFile(configFile)
	require.NoError(err)
	require.Equal(newContent, string(finalData))

	// No leftover temp files in dir
	entries, err := os.ReadDir(filepath.Dir(configFile))
	require.NoError(err)
	require.Len(entries, 1)
	require.Equal("config", entries[0].Name())
}

func TestEditConfig_InvalidConfigPreservesOriginal(t *testing.T) {
	require := require.New(t)
	c, configFile := setupTestConfigCmd(t)

	initialContent := "cache-dir: /original/cache\n"
	require.NoError(os.WriteFile(configFile, []byte(initialContent), 0600))

	// Case 1: Unknown field, user aborts on re-open by leaving file unchanged (:q)
	callCount := 0
	mockEditor(t, func(editedFilePath string) error {
		callCount++
		if callCount == 1 {
			return os.WriteFile(editedFilePath, []byte("unknown-option: true\n"), 0600)
		}
		// Second call (re-opened with error banner): leave file unmodified to abort
		return nil
	})

	c.edit = true
	err := c.run(context.Background())
	require.Error(err)
	require.Contains(err.Error(), "invalid config")
	require.Equal(2, callCount)

	// Original file must remain intact
	data, err := os.ReadFile(configFile)
	require.NoError(err)
	require.Equal(initialContent, string(data))

	// Rejected file must contain the unapplied edits without the error header
	rejectedData, err := os.ReadFile(configFile + ".rejected")
	require.NoError(err)
	require.Equal("unknown-option: true\n", string(rejectedData))

	// Case 2: Invalid semantic config (rbe-as-cas without rbe-instance), aborting with :cq (editor error on retry)
	callCount = 0
	mockEditor(t, func(editedFilePath string) error {
		callCount++
		if callCount == 1 {
			return os.WriteFile(editedFilePath, []byte("rbe-as-cas: true\n"), 0600)
		}
		return os.ErrInvalid
	})

	err = c.run(context.Background())
	require.Error(err)
	require.Contains(err.Error(), "invalid config")
	require.Equal(2, callCount)

	// Original file must still remain intact
	data, err = os.ReadFile(configFile)
	require.NoError(err)
	require.Equal(initialContent, string(data))

	// Rejected file updated with latest unapplied edits
	rejectedData, err = os.ReadFile(configFile + ".rejected")
	require.NoError(err)
	require.Equal("rbe-as-cas: true\n", string(rejectedData))
}

func TestEditConfig_ReopenAndFix(t *testing.T) {
	require := require.New(t)
	c, configFile := setupTestConfigCmd(t)

	initialContent := "cache-dir: /old/cache\n"
	require.NoError(os.WriteFile(configFile, []byte(initialContent), 0600))

	callCount := 0
	var secondCallSawHeader bool

	mockEditor(t, func(editedFilePath string) error {
		callCount++
		if callCount == 1 {
			// Introduce a typo on first edit
			return os.WriteFile(editedFilePath, []byte("cahce-dir: /fixed/cache\n"), 0600)
		}
		// Second call: verify error banner is present at top of file, fix typo while leaving banner intact
		data, err := os.ReadFile(editedFilePath)
		if err != nil {
			return err
		}
		content := string(data)
		secondCallSawHeader = strings.Contains(content, "VALIDATION ERROR")
		fixed := strings.ReplaceAll(content, "cahce-dir:", "cache-dir:")
		return os.WriteFile(editedFilePath, []byte(fixed), 0600)
	})

	c.edit = true
	require.NoError(c.run(context.Background()))
	require.Equal(2, callCount)
	require.True(secondCallSawHeader)

	// Final config file should have the fixed config with the error header stripped
	finalData, err := os.ReadFile(configFile)
	require.NoError(err)
	require.Equal("cache-dir: /fixed/cache\n", string(finalData))

	// Any previous .rejected file should be cleaned up on success
	_, err = os.Stat(configFile + ".rejected")
	require.True(os.IsNotExist(err))
}

func TestEditConfig_NewFile(t *testing.T) {
	require := require.New(t)
	c, configFile := setupTestConfigCmd(t)

	newContent := "local-only: true\n"
	mockEditor(t, func(editedFilePath string) error {
		return os.WriteFile(editedFilePath, []byte(newContent), 0600)
	})

	c.edit = true
	require.NoError(c.run(context.Background()))

	data, err := os.ReadFile(configFile)
	require.NoError(err)
	require.Equal(newContent, string(data))
}

func TestEditConfig_MutuallyExclusiveFlags(t *testing.T) {
	require := require.New(t)
	c, _ := setupTestConfigCmd(t)

	fl := pflag.NewFlagSet("config", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	require.NoError(fl.Parse([]string{"-e", "-w"}))

	err := c.Exec(context.Background(), fl)
	require.Error(err)
	require.Contains(err.Error(), "Only one of")
}

func TestEditConfig_VimSyntaxAndAutocomplete(t *testing.T) {
	require := require.New(t)
	c, configFile := setupTestConfigCmd(t)

	oldExec := editor.ExecCommand
	t.Cleanup(func() {
		editor.ExecCommand = oldExec
	})
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "vim")

	var capturedArgs []string
	var vimScriptContent string

	editor.ExecCommand = func(name string, arg ...string) *exec.Cmd {
		if name == "vim" {
			capturedArgs = append([]string{}, arg...)
			for i := 0; i < len(arg)-1; i++ {
				if arg[i] == "-S" {
					data, err := os.ReadFile(arg[i+1])
					if err == nil {
						vimScriptContent = string(data)
					}
				}
			}
			targetFile := arg[len(arg)-1]
			_ = os.WriteFile(targetFile, []byte("cache-dir: /tmp/vim-test\n"), 0600)
			return exec.Command("true")
		}
		return oldExec(name, arg...)
	}

	c.edit = true
	require.NoError(c.run(context.Background()))

	require.GreaterOrEqual(len(capturedArgs), 3)
	require.Equal("-S", capturedArgs[0])
	require.True(filepath.Ext(capturedArgs[len(capturedArgs)-1]) == ".yaml")

	require.Contains(vimScriptContent, "setlocal filetype=yaml")
	require.Contains(vimScriptContent, "setlocal iskeyword+=-,.")
	require.Contains(vimScriptContent, "function! AbfsConfigComplete(findstart, base) abort")
	require.Contains(vimScriptContent, "'key': 'cache-dir'")
	require.Contains(vimScriptContent, "'key': 'fs'")
	require.Contains(vimScriptContent, "'word': 'kmod'")

	// Ensure all temporary .vim, .dict, and .yaml files were cleaned up
	entries, err := os.ReadDir(filepath.Dir(configFile))
	require.NoError(err)
	require.Len(entries, 1)
	require.Equal("config", entries[0].Name())
}
