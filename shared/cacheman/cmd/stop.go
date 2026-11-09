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
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cacheman/client"
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

var (
	// stopTimeout is how long stop waits after SIGINT before it sends SIGKILL.
	stopTimeout = 30 * time.Second
	// killTimeout is how long stop waits for the exit after SIGKILL.
	killTimeout = 5 * time.Second
	// geteuid is a variable so that tests can act as another user.
	geteuid = os.Geteuid
)

func stopCachemanInst(ctx context.Context, inst *instance.Instance) error {
	peer, found, err := findCacheman(inst)
	if errors.Is(err, errListenQueueFull) {
		socket := client.HTTPSocketPath(inst.Config.CacheDir)
		return fmt.Errorf("%w, so its pid is unknown; find it with 'ss -xlp src %s' and stop it with kill", err, socket)
	}
	if err != nil {
		return err
	}
	if !found {
		// Nothing to stop.
		return nil
	}
	if peer.Pid == 0 {
		return fmt.Errorf("cacheman for instance %q runs in another pid namespace, so it cannot be stopped from here", inst.Name)
	}
	if euid := geteuid(); peer.UID != euid && euid != 0 {
		return fmt.Errorf("cacheman for instance %q (pid %d) runs as uid %d, so uid %d cannot stop it", inst.Name, peer.Pid, peer.UID, euid)
	}

	process, err := os.FindProcess(peer.Pid)
	if err != nil {
		return err
	}

	// Send SIGINT first to attempt a graceful shutdown.
	if err := process.Signal(syscall.SIGINT); err != nil {
		return err
	}
	if exited, err := waitForExit(ctx, process, stopTimeout); exited || err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGKILL); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	// The socket lock and the cache stay held until the process is gone, and restart
	// starts the new cacheman next.
	exited, err := waitForExit(ctx, process, killTimeout)
	if err == nil && !exited {
		return fmt.Errorf("cacheman (pid %d) did not exit after SIGKILL", peer.Pid)
	}
	return err
}

// waitForExit polls until process has exited or timeout expires.
func waitForExit(ctx context.Context, process *os.Process, timeout time.Duration) (bool, error) {
	timeoutTime := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case t := <-ticker.C:
			if processExited(process) {
				return true, nil
			}
			if t.After(timeoutTime) {
				return false, nil
			}
		}
	}
}

// processExited reports whether process has exited. A process that has exited but
// that its parent has not reaped yet still takes signals, so a zombie counts as
// exited. os.FindProcess holds a pidfd, so the signal cannot reach a reused pid. If
// the pid is reaped and reused between the signal and the /proc read, the next call
// sees the exit.
func processExited(process *os.Process) bool {
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return true
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", process.Pid))
	if err != nil {
		return false
	}
	// The state follows the command name, which is in parentheses and may contain
	// spaces or parentheses itself.
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) || stat[i+2] != 'Z' {
		return false
	}
	// The main thread is a zombie as soon as it exits, while other threads may still
	// be exiting and hold the open files, among them the socket and its lock. A thread
	// leaves the task list only after it has closed them, so the process is gone once
	// the main thread is the only one left.
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", process.Pid))
	return err == nil && len(tasks) == 1
}

func (c *stopCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	return stopCachemanInst(ctx, c.Instance)
}
