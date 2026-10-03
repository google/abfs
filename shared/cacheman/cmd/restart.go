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
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
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

	if c.sysRestart != nil {
		if err := c.sysRestart.Exec(ctx, fl); err != nil {
			return err
		}
	} else {
		// Blindly try to stop.
		if err := stopCachemanInst(ctx, c.Instance); err != nil {
			return err
		}

		executablePath, err := os.Executable()
		if err != nil {
			log.Fatalf("Error getting executable path: %v", err)
		}

		cmd := exec.CommandContext(ctx, executablePath, "--instance", c.Instance.Name, "--"+c.Instance.Flavor.InstancesDirOpt, c.Instance.BaseDir, "cacheman", "run")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
	}
	if c.sysRestart == nil {
		fmt.Println("Restart cacheman done.")
	}

	checkName := "remote-ok"
	checkFunc := func() error { return c.Cacheman.RemoteOK() }
	if c.Config != nil && c.Config.LocalOnly {
		checkName = "ping"
		checkFunc = func() error { return c.Cacheman.Ping() }
	}

	fmt.Printf("Waiting for cacheman to respond to %s...\n", checkName)
	return c.waitFor(ctx, checkName, checkFunc)
}

func (c *restartCmd) waitFor(ctx context.Context, name string, check func() error) error {
	if c.CachemanProvider == nil || c.Cacheman == nil {
		return errors.New("cacheman client not initialized")
	}

	timeoutTime := time.Now().Add(c.timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		err := check()
		if err == nil {
			return nil
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
