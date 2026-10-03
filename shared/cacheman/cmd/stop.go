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
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/instance"
)

type stopCmd struct {
	*cachemanCmd
}

func NewStopCmd(c *cachemanCmd) *stopCmd {
	return &stopCmd{cachemanCmd: c}
}

func (c *stopCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "stop",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Stops a 'cacheman run' process for the detected instance.`,
	}
}

func (c *stopCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func stopCachemanInst(ctx context.Context, inst *instance.Instance) error {
	currInstanceOnly := true
	cachemans, err := loadCachemans(inst, currInstanceOnly)
	if err != nil {
		return err
	}
	if len(cachemans.infos) != 1 {
		// Nothing to stop.
		return nil
	}

	pid := cachemans.infos[0].pid
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	// Send SIGINT first to attempt a graceful shutdown.
	if err := process.Signal(syscall.SIGINT); err != nil {
		return err
	}

	timeoutTime := time.Now().Add(30 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			// If sending signal 0 fails, we can assume the process isn't running.
			if err := process.Signal(syscall.Signal(0)); err != nil {
				return nil
			}

			// If we timeout, send sigkill.
			if t.After(timeoutTime) {
				return process.Signal(syscall.SIGKILL)
			}
		}
	}
}

func (c *stopCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	return stopCachemanInst(ctx, c.Instance)
}
