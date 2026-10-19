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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cacheman/server"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/systemd"
	"github.com/google/abfs/shared/user"
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

func instanceSocketLockPath(inst *instance.Instance) string {
	return path.Join(inst.Config.CacheDir, "socket.lock")
}

func socketLockPathForPid(pid int) (string, error) {
	fdPath := path.Join("/proc", strconv.Itoa(pid), "fd")

	files, err := os.ReadDir(fdPath)
	if err != nil {
		return "", fmt.Errorf("error reading %q: %w", fdPath, err)
	}

	for _, file := range files {
		symlinkPath := filepath.Join(fdPath, file.Name())
		targetPath, err := os.Readlink(symlinkPath)
		if err != nil {
			return "", fmt.Errorf("error resolving symlink %q: %w", symlinkPath, err)
		}
		if strings.Contains(targetPath, "socket.lock") {
			return targetPath, nil
		}
	}

	return "", errors.New("process isn't using a socket.lock file")
}

type cachemans struct {
	psHeader string
	infos    []cachemanInfo
}

type cachemanInfo struct {
	pid            int
	psLine         string
	socketLockPath string
}

// loadCachemans returns cachemans for the current user.
func loadCachemans(inst *instance.Instance, currInstanceOnly bool) (*cachemans, error) {
	var infos []cachemanInfo

	currLockPath := instanceSocketLockPath(inst)
	// Providing -U to 'ps' will perform a logical OR with the -C option.
	// The user filter is done manually.
	output, err := exec.Command("ps", "-C", "abfs", "-o", "uname:32,pid,etime,cmd").Output()
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	currUser, err := user.CurrentUser()
	if err != nil {
		return nil, err
	}
	var header string
	for scanner.Scan() {
		if header == "" {
			header = scanner.Text()
		}
		if strings.Contains(scanner.Text(), "cacheman run") {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 3 {
				return nil, fmt.Errorf("could not parse %q", scanner.Text())
			}
			// The returned username from ps is capped to 32 characters above.
			// Using HasPrefix accounts for usernames longer than 32 characters.
			if !strings.HasPrefix(currUser.Username, fields[0]) {
				continue
			}
			pid, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, err
			}
			socketLock, err := socketLockPathForPid(pid)
			if err != nil {
				return nil, err
			}

			if currInstanceOnly && currLockPath != socketLock {
				continue
			}

			infos = append(infos, cachemanInfo{
				pid:            pid,
				psLine:         scanner.Text(),
				socketLockPath: socketLock,
			})
		}
	}
	if scanner.Err() != nil {
		return nil, err
	}

	return &cachemans{psHeader: header, infos: infos}, nil
}
