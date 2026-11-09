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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/instance"
)

type restartCmd struct {
	*cachemanCmd
	*cmd.CachemanProvider
	timeout    time.Duration
	sysRestart cmd.ABFSCmd
}

func NewRestartCmd(c *cachemanCmd, sysRestart cmd.ABFSCmd) *restartCmd {
	return &restartCmd{cachemanCmd: c, sysRestart: sysRestart}
}

func (c *restartCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "restart",
		Usage:   "[-t <timeout in seconds>]",
		Aliases: []string{},
		Desc:    `Restart (or start) cacheman for the detected instance.`,
	}
}

func (c *restartCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.DurationVarP(&c.timeout, "timeout", "t", 2*time.Minute, "Timeout waiting for cacheman to respond")
}

func (c *restartCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	checkName := "remote-ok"
	checkFunc := func() error { return c.Cacheman.RemoteOK() }
	if c.Config != nil && c.Config.LocalOnly {
		checkName = "ping"
		checkFunc = func() error { return c.Cacheman.Ping() }
	}

	if c.sysRestart != nil {
		if err := c.sysRestart.Exec(ctx, fl); err != nil {
			return err
		}
	} else {
		// Blindly try to stop.
		if err := stopCachemanInst(ctx, c.Instance); err != nil {
			return err
		}

		pid, err := runDetached(c.Instance)
		if err != nil {
			return err
		}
		fmt.Println("Restart cacheman done.")
		started, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		outPath := cachemanOutPath(c.Instance)
		answers := checkFunc
		checkFunc = func() error {
			// The new cacheman is our child until we exit, so it stays a zombie when it
			// fails and the check can tell at once.
			if processExited(started) {
				return fmt.Errorf("%w before it answered %s; see %s", errCachemanExited, checkName, outPath)
			}
			return answers()
		}
	}

	fmt.Printf("Waiting for cacheman to respond to %s...\n", checkName)
	return c.waitFor(ctx, checkName, checkFunc)
}

func (c *restartCmd) waitFor(ctx context.Context, name string, check func() error) error {
	if c.CachemanProvider == nil || c.Cacheman == nil {
		return errors.New("cacheman client not initialized")
	}
	return waitForCacheman(ctx, c.timeout, name, check)
}

// waitForCacheman polls check until it succeeds or timeout expires.
func waitForCacheman(ctx context.Context, timeout time.Duration, name string, check func() error) error {
	timeoutTime := time.Now().Add(timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		err := check()
		if err == nil {
			return nil
		}
		if errors.Is(err, errCachemanExited) {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			if t.After(timeoutTime) {
				return fmt.Errorf("timeout waiting for cacheman to respond to %s: %w", name, err)
			}
		}
	}
}

var errCachemanExited = errors.New("cacheman exited")

// newRunCmd returns the command that runs cacheman for inst. Tests replace it.
var newRunCmd = func(inst *instance.Instance) (*exec.Cmd, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to find the abfs executable: %w", err)
	}
	return exec.Command(executablePath, "--instance", inst.Name, inst.Flavor.DashedInstancesDir(), inst.BaseDir, "cacheman", "run"), nil
}

// cachemanOutPath returns the file that gets the stdout and stderr of cacheman, the
// one the systemd unit uses.
func cachemanOutPath(inst *instance.Instance) string {
	return filepath.Join(inst.Config.LogDir, "cachemand")
}

// runDetached starts 'cacheman run' for inst so that it outlives this command, and
// returns its pid.
func runDetached(inst *instance.Instance) (int, error) {
	if err := os.MkdirAll(inst.Config.LogDir, 0700); err != nil {
		return 0, err
	}
	cmd, err := newRunCmd(inst)
	if err != nil {
		return 0, err
	}
	return startDetached(cmd, cachemanOutPath(inst))
}

// startDetached starts cmd in a session of its own with stdin from /dev/null and its
// output appended to outPath, and returns its pid. No context is attached, so the end
// of this command does not kill it, and the terminal sends it no SIGINT or SIGHUP.
// It does not hold the output of the caller, so 'out=$(abfs cacheman restart)'
// returns.
func startDetached(cmd *exec.Cmd, outPath string) (int, error) {
	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return 0, err
	}
	defer func() { _ = out.Close() }()
	cmd.Stdin = nil
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	return pid, cmd.Process.Release()
}
