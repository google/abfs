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

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/instance"
)

const helperModeEnv = "ABFS_CLIEXEC_HELPER_MODE"

// helperCmd is the command TestHelperProcess runs through CliExec.Run.
type helperCmd struct {
	mode string
}

func (c *helperCmd) Spec() CmdSpec {
	return CmdSpec{Name: "helper"}
}

func (c *helperCmd) RegisterFlags(fl *pflag.FlagSet) {}

func (c *helperCmd) ResolveInstance() *instance.Instance {
	return &instance.Instance{Config: &instance.Config{}}
}

func (c *helperCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	switch c.mode {
	case "fail":
		return errors.New("failed without a signal")
	case "done":
		return nil
	}
	fmt.Println("ready")
	<-ctx.Done()
	switch c.mode {
	case "canceled":
		return ctx.Err()
	case "no-message":
		return clierr.CLIErrNoMessage(1)
	}
	return nil
}

// TestHelperProcess is not a test: runHelper starts the test binary with it to run a
// command through CliExec.Run, so the exit code is the real one.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}
	c := &cliCommand{abfsCmd: &helperCmd{mode: mode}}
	c.Spec()
	c.Run(pflag.NewFlagSet("helper", pflag.ContinueOnError))
	os.Exit(0)
}

// runHelper runs the helper command, sends sig once it is ready (if sig is not zero),
// and returns its exit code and stderr.
func runHelper(t *testing.T, mode string, sig syscall.Signal) (int, string) {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	c.Env = append(os.Environ(), helperModeEnv+"="+mode)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	stdout, err := c.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, c.Start())

	if sig != 0 {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "ready\n", line)
		require.NoError(t, c.Process.Signal(sig))
	}
	_, _ = io.Copy(io.Discard, stdout)

	err = c.Wait()
	// The race detector reports at exit only for exit code 0, so check its output here.
	require.NotContains(t, stderr.String(), "DATA RACE")
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stderr.String()
	}
	require.NoError(t, err)
	return 0, stderr.String()
}

func TestCliExec_ExitCode(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		sig        syscall.Signal
		wantCode   int
		wantStderr string
	}{
		{name: "failure without a signal", mode: "fail", wantCode: 1, wantStderr: "failed without a signal"},
		{name: "success without a signal", mode: "done", wantCode: 0},
		{name: "error after SIGTERM", mode: "canceled", sig: syscall.SIGTERM, wantCode: 143, wantStderr: "interrupted by SIGTERM: context canceled"},
		{name: "error after SIGINT", mode: "canceled", sig: syscall.SIGINT, wantCode: 130, wantStderr: "interrupted by SIGINT: context canceled"},
		{name: "error without a message after SIGTERM", mode: "no-message", sig: syscall.SIGTERM, wantCode: 143, wantStderr: "interrupted by SIGTERM\n"},
		{name: "clean stop after SIGTERM", mode: "stop", sig: syscall.SIGTERM, wantCode: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stderr := runHelper(t, tt.mode, tt.sig)
			require.Equal(t, tt.wantCode, code, "stderr: %s", stderr)
			require.Contains(t, stderr, tt.wantStderr)
		})
	}
}
