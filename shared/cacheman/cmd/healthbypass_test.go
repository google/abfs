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

package cacheman

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/cacheman/server"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/instance"
)

func setupTestCachemanCmd(t *testing.T) (*cachemanCmd, string) {
	t.Helper()
	cacheDir := t.TempDir()
	inst := &instance.Instance{
		Name: "test",
		Config: &instance.Config{
			CacheDir: cacheDir,
		},
	}
	rootCmd := root.NewRootCommand()
	c := &cachemanCmd{
		RootCmd:        rootCmd,
		ConfigProvider: &cmd.ConfigProvider{Instance: inst},
	}
	return c, cacheDir
}

func TestHealthBypassCmd_SetClearShow(t *testing.T) {
	c, cacheDir := setupTestCachemanCmd(t)
	healthBypass := NewHealthBypassCmd(c)
	setCmd := NewHealthBypassSetCmd(healthBypass)
	clearCmd := NewHealthBypassClearCmd(healthBypass)
	showCmd := NewHealthBypassShowCmd(healthBypass)

	bypassFile := filepath.Join(cacheDir, server.StatusDirName, server.HealthBypassFileName)

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)

	// 1. Initially disabled (file does not exist)
	require.NoError(t, showCmd.Exec(context.Background(), flags))
	require.NoError(t, healthBypass.Exec(context.Background(), flags))
	_, err := os.Stat(bypassFile)
	require.True(t, os.IsNotExist(err))

	// 2. Run 'set' -> file is created
	require.NoError(t, setCmd.Exec(context.Background(), flags))
	_, err = os.Stat(bypassFile)
	require.NoError(t, err)

	// 3. Run 'show' while enabled
	require.NoError(t, showCmd.Exec(context.Background(), flags))
	require.NoError(t, healthBypass.Exec(context.Background(), flags))

	// 4. Run 'clear' -> file is removed
	require.NoError(t, clearCmd.Exec(context.Background(), flags))
	_, err = os.Stat(bypassFile)
	require.True(t, os.IsNotExist(err))

	// 5. Run 'clear' again when already cleared -> no error
	require.NoError(t, clearCmd.Exec(context.Background(), flags))
}
