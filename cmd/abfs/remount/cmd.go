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

package remount

import (
	"context"

	"github.com/spf13/pflag"

	"github.com/google/abfs/cmd/abfs/doctor"
	"github.com/google/abfs/cmd/abfs/ws"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
)

type daemonHolder interface {
	CloseDaemon() error
}

type mountCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	*cmd.CachemanProvider
	foreground            bool
	ws                    string
	skipHealthCheck       bool
	delegatedDaemonHolder daemonHolder
}

func NewCmd(rootCmd *root.RootCmd) *mountCmd {
	return &mountCmd{RootCmd: rootCmd}
}

func (c *mountCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "remount",
		Usage:   `<mount-point> [--ws <workspace-name>] [--foreground]`,
		Aliases: []string{},
		Desc:    `Re-mount an existing workspace at the given mount-point directory.`,
	}
}

func (c *mountCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.foreground, "foreground", false, "Run the command in foreground")
	fl.StringVar(&c.ws, "ws", "", "The name of the new workspace")
	fl.BoolVar(&c.skipHealthCheck, "skip-health-check", false, "Skip health check before remounting the workspace")
}

func (c *mountCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	mountPoint := args.EnsureSingleArg(fl)

	if !c.skipHealthCheck {
		if err := doctor.PreflightCheck(c.Instance); err != nil {
			return err
		}
	}

	mountRepoCmd := ws.NewMountCmd(c.RootCmd, ws.MountOpts{
		WSName:     c.ws,
		MountPoint: mountPoint,
		Foreground: c.foreground,
	})
	mountRepoCmd.ConfigProvider = c.ConfigProvider
	mountRepoCmd.StoreProvider = c.StoreProvider
	mountRepoCmd.CachemanProvider = c.CachemanProvider
	c.delegatedDaemonHolder = mountRepoCmd
	return mountRepoCmd.Run(ctx)
}

func (c *mountCmd) CloseDaemon() error {
	if c.delegatedDaemonHolder != nil {
		return c.delegatedDaemonHolder.CloseDaemon()
	}
	return nil
}
