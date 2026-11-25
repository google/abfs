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
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/instance"
)

type statusCmd struct {
	*cachemanCmd
}

func NewStatusCmd(c *cachemanCmd) *statusCmd {
	return &statusCmd{cachemanCmd: c}
}

func (c *statusCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "status",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Display cacheman status for the detected instance.`,
	}
}

func (c *statusCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *statusCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	running, err := writeStatus(os.Stdout, os.Stderr, c.Instance)
	if err != nil {
		return err
	}
	if !running {
		// systemd 'status' will exit with error if the process isn't running.
		os.Exit(1)
	}
	return nil
}

// writeStatus prints the cacheman that listens on the socket of inst and reports
// whether one listens. A cacheman that listens but does not answer is running, as
// systemd sees an active unit, so it only gets a warning. stdout gets only the
// columns of 'ps -o uname:32,pid,etime,cmd', which scripts may read; every other
// message goes to stderr.
func writeStatus(stdout, stderr io.Writer, inst *instance.Instance) (bool, error) {
	cacheDir := inst.Config.CacheDir
	socket := client.HTTPSocketPath(cacheDir)
	peer, found, err := findCacheman(inst)
	if errors.Is(err, errListenQueueFull) {
		fmt.Fprintf(stderr, "Warning: %v\n", err)
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !found {
		fmt.Fprintf(stderr, "cacheman for instance %q is not running: nothing listens on %s\n", inst.Name, socket)
		return false, nil
	}

	desc := "cacheman"
	if peer.Pid == 0 {
		fmt.Fprintf(stderr, "cacheman for instance %q listens on %s; its process is in another pid namespace\n", inst.Name, socket)
	} else {
		desc = fmt.Sprintf("cacheman (pid %d)", peer.Pid)
		if out, ok := psLine(peer.Pid); ok {
			_, _ = stdout.Write(out)
		} else {
			fmt.Fprintf(stderr, "cacheman for instance %q listens on %s with pid %d\n", inst.Name, socket, peer.Pid)
		}
	}
	if euid := geteuid(); peer.UID != euid {
		fmt.Fprintf(stderr, "Warning: %s runs as uid %d, not as uid %d\n", desc, peer.UID, euid)
	}

	if err := client.NewHTTPClientWithTimeout(cacheDir, cachemanAnswerTimeout).Ping(); err != nil {
		fmt.Fprintf(stderr, "Warning: %s does not answer on %s: %v\n", desc, socket, err)
	}
	return true, nil
}

// psLine returns the ps header and the line of pid. ok is false when ps fails or
// prints another format, as BusyBox ps does.
func psLine(pid int) (out []byte, ok bool) {
	out, err := exec.Command("ps", "-o", "uname:32,pid,etime,cmd", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil, false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || !slices.Equal(strings.Fields(lines[0]), []string{"USER", "PID", "ELAPSED", "CMD"}) {
		return nil, false
	}
	if fields := strings.Fields(lines[1]); len(fields) < 2 || fields[1] != strconv.Itoa(pid) {
		return nil, false
	}
	return out, true
}
