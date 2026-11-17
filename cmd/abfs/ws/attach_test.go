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

package ws

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
)

func TestRestoresSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		isNew    bool
		strategy binding.PutStrategy
		want     bool
	}{
		{name: "remount", strategy: binding.PutStrategyRemoteWin, want: true},
		{name: "new workspace after repo sync", isNew: true, strategy: binding.PutStrategyExactMatch},
		{name: "local wins", strategy: binding.PutStrategyLocalWin},
		{name: "exact match", strategy: binding.PutStrategyExactMatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, restoresSnapshot(tt.isNew, tt.strategy))
		})
	}
}

func TestAutoSetup(t *testing.T) {
	var ran string
	require.NoError(t, autoSetup(context.Background(), "/mnt/ws", true, func(_ context.Context, dir string) error {
		ran = dir
		return nil
	}))
	require.Equal(t, "/mnt/ws", ran)

	failed := func(context.Context, string) error { return errors.New("exit status 1") }
	require.EqualError(t, autoSetup(context.Background(), "/mnt/ws", true, failed),
		"auto-setup failed (the workspace is attached, run abfs setup /mnt/ws): exit status 1", "ws attach run by hand")
	require.EqualError(t, autoSetup(context.Background(), "/mnt/ws", false, failed),
		"auto-setup failed: exit status 1", "the remount unmounts the workspace, so it gives no hint")
}

func TestAttachFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "by hand", args: []string{"-s", "remote-wins", "ws", "/mnt/ws"}},
		{name: "from the mount", args: []string{"-s", "remote-wins", "--from-mount", "ws", "/mnt/ws"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &attachCmd{}
			fl := pflag.NewFlagSet("attach", pflag.ContinueOnError)
			c.RegisterFlags(fl)
			require.NoError(t, fl.Parse(tt.args))
			require.Equal(t, tt.want, c.fromMount)
			require.True(t, fl.Lookup("from-mount").Hidden)
		})
	}
}

func TestIsMountPoint(t *testing.T) {
	require.True(t, isMountPoint("/"))
	require.False(t, isMountPoint(t.TempDir()))
}
