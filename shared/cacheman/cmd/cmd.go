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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cacheman/server"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/systemd"
)

type cachemanCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	SysCmd  *systemd.SystemdCmd
	newARES server.NewARESFunc
}

// servicer is an empty wrapper struct just to return the cacheman service
type servicer struct {
	*cachemanCmd
}

var _ systemd.Servicer = (*servicer)(nil)

func (s servicer) Name() string {
	return "Cacheman"
}

func (s servicer) Service() systemd.Service {
	inst := s.Instance
	config := inst.Config

	abfsExecutable, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("Could not determine current executable path: %v", err))
	}

	cachemanLogFile := filepath.Join(config.LogDir, "cacheman")

	// Construct the command with the --instance flag so the daemon knows which instance it is.
	command := fmt.Sprintf("%s --instance %s %s %s cacheman run --logfile %s",
		abfsExecutable,
		inst.Name,
		inst.Flavor.DashedInstancesDir(),
		inst.BaseDir,
		cachemanLogFile,
	)

	return systemd.Service{
		// The service name is unique per instance (e.g., "cacheman@default", "cacheman@other").
		Name:        fmt.Sprintf("cacheman@%s", inst.Name),
		Description: fmt.Sprintf("The ABFS Cacheman service for instance '%s'", inst.Name),
		Command:     command,
		LogFile:     cachemanLogFile,
		StdoutFile:  filepath.Join(config.LogDir, "cachemand"),
	}
}

func NewCmd(rootCmd *root.RootCmd, newARES server.NewARESFunc) *cachemanCmd {
	return NewCmdWithServicer(rootCmd, newARES, nil)
}

func NewCmdWithServicer(rootCmd *root.RootCmd, newARES server.NewARESFunc, srv systemd.Servicer) *cachemanCmd {
	// Create the cacheman command struct.
	c := &cachemanCmd{
		RootCmd: rootCmd,
		newARES: newARES,
	}

	if srv == nil {
		srv = &servicer{cachemanCmd: c}
	}

	// Create the SystemdCmd helper, providing it with our instance-aware servicer.
	c.SysCmd = systemd.NewSystemdCmd(rootCmd, srv)

	return c
}

func (c *cachemanCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "cacheman",
		Usage:       "",
		Desc:        "Start the cacheman process to manage the ABFS local cache.",
		Subcommands: c.subcommands(),
	}
}

func (c *cachemanCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *cachemanCmd) subcommands() []cmd.ABFSCmd {
	commands := []cmd.ABFSCmd{
		NewRunCmd(c),
		NewInitCmd(c),
		NewClientCmd(c),
		NewCleanCmd(c),
		NewWaitCmd(c),
		NewPingCmd(c),
		NewRemoteOkCmd(c),
		NewValidateCmd(c),
		NewStatsCmd(c),
		NewConfigCmd(c),
		NewDebugCmd(c),
		NewPrefetchCmd(c),
		NewHealthBypassCmd(c),
	}
	var sysRestart cmd.ABFSCmd
	if SystemctlSupported() {
		// We custom wrap the restart command to wait for cacheman to come back online.
		for _, sub := range c.SysCmd.Subcommands() {
			if sub.Spec().Name != "restart" {
				commands = append(commands, sub)
			} else {
				sysRestart = sub
			}
		}
	} else {
		commands = append(commands,
			NewStatusCmd(c),
			NewStopCmd(c),
			NewTailCmd(c),
		)
	}
	commands = append(commands, NewRestartCmd(c, sysRestart))

	return commands
}

func (c *cachemanCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return clierr.NoSubcommandErr(fl)
}

// SystemctlSupported reports whether a systemd user manager is reachable via
// `systemctl --user`.
func SystemctlSupported() bool {
	return checkSystemctlUser() == nil
}

// checkSystemctlUser runs `systemctl --user` and, if it fails, returns an error
// that includes systemctl's output so callers can tell the user why.
func checkSystemctlUser() error {
	out, err := exec.Command("systemctl", "--user").CombinedOutput()
	if out = bytes.TrimSpace(out); err != nil && len(out) > 0 {
		return fmt.Errorf("%w: %s", err, out)
	}
	return err
}
