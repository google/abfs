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

package gitpusher

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"github.com/google/abfs/gitpusher"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/hashpath"
)

type gitPusherCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	configHashPath string
	gitBin         string
	storagePath    string
	instance       string
}

func NewCmd(rootCmd *root.RootCmd) *gitPusherCmd {
	return &gitPusherCmd{RootCmd: rootCmd}
}

func (c *gitPusherCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "git-pusher",
		Usage:   "[-c <config-hash-path>] [-i <instance-name>]",
		Aliases: []string{},
		Desc:    `Starts a daemon that will monitor the provided branches and push all git content to the ABFS servers.`,
	}
}

func (c *gitPusherCmd) RegisterFlags(fl *pflag.FlagSet) {
	hostname, err := os.Hostname()
	if err != nil {
		panic(err)
	}

	fl.StringVarP(&c.configHashPath, "config", "c", "config:git-pusher/default", "The hash-path of the git-pusher config")
	fl.StringVarP(&c.gitBin, "git-bin", "g", "/usr/bin/git", "Path to `git` binary")
	fl.StringVarP(&c.storagePath, "storage-path", "s", "$HOME/git-pusher", "Path where the projects should be stored")
	fl.StringVarP(&c.instance, "instance", "i", hostname, "The git-pusher instance")
}

func (c *gitPusherCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	return c.run(ctx)
}

func (c *gitPusherCmd) run(ctx context.Context) error {
	config := c.Config

	hp, err := hashpath.Parse(c.configHashPath)
	if err != nil {
		return fmt.Errorf("failed to parse hash-path %s: %w", c.configHashPath, err)
	}

	c.storagePath = os.ExpandEnv(c.storagePath)
	if err := os.MkdirAll(c.storagePath, 0755); err != nil {
		return fmt.Errorf("failed to create project-storage-path at %s: %w", c.storagePath, err)
	}

	c.gitBin = os.ExpandEnv(c.gitBin)
	gitFileInfo, err := os.Stat(c.gitBin)
	if err != nil {
		return fmt.Errorf("failed to read git-bin at %s: %w", c.gitBin, err)
	}
	if gitFileInfo.IsDir() {
		return fmt.Errorf("provided git-bin %s is a directory", c.gitBin)
	}
	if gitFileInfo.Mode()&0111 == 0 {
		return fmt.Errorf("git at %s is not executable", c.gitBin)
	}

	pusher := gitpusher.NewGitPusher(&gitpusher.NewGitPusherArgs{
		InstanceName: c.instance,
		ConfigHP:     hp,
		GitBin:       c.gitBin,
		StoragePath:  c.storagePath,
		Servers:      config.EffectiveRemoteServers(),
		AuthInfo:     config.GetAuthInfo(),
	})

	return pusher.Run(ctx)
}
