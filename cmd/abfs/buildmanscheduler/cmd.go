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

package buildmanscheduler

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/systemd"
)

type buildmanSchedulerCmd struct {
	*root.RootCmd
	sysCmd       *systemd.SystemdCmd
	logPath      string
	serviceSetup sync.Once
	service      systemd.Service
}

func NewCmd(rootCmd *root.RootCmd) *buildmanSchedulerCmd {
	c := &buildmanSchedulerCmd{RootCmd: rootCmd}
	c.sysCmd = systemd.NewSystemdCmd(rootCmd, c)
	return c
}

func (c *buildmanSchedulerCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "buildmanscheduler",
		Usage:       "",
		Aliases:     []string{},
		Desc:        "Start the builman scheduler to periodically schedule builds",
		Subcommands: c.subcommands(),
	}
}

func (c *buildmanSchedulerCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.logPath, "log", "l", "", "The path to the log file (defaults to the instance log directory)")
}

func (c *buildmanSchedulerCmd) subcommands() []cmd.ABFSCmd {
	return append(c.sysCmd.Subcommands(), NewRunCmd(c), NewScheduleCmd(c))
}

func (c *buildmanSchedulerCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	fl.Usage()
	return clierr.NoSubcommandErr(fl)
}

func (c *buildmanSchedulerCmd) Name() string {
	return "buildmanscheduler"
}

func (c *buildmanSchedulerCmd) Service() systemd.Service {
	c.serviceSetup.Do(func() {
		execPath, err := os.Executable()
		if err != nil {
			panic(err)
		}
		c.service = systemd.Service{
			Name:        c.Name(),
			Description: "The ABFS buildman scheduler service",
			Command:     fmt.Sprintf("%s buildmanscheduler run", execPath),
			LogFile:     os.ExpandEnv(c.logPath),
		}
	})
	return c.service
}
