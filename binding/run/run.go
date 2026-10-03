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

package run

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

type RunResult struct {
	Timestamp   time.Time
	Pid         int
	ExitCode    int
	Rusage      *syscall.Rusage
	RunDuration float64
	Stdout      []byte
	Stderr      string
	Err         error
}

func BuildCommand(cmd string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", cmd)
}

func RunCommand(cmd *exec.Cmd) (r RunResult) {
	r.Timestamp = time.Now()
	defer func() {
		r.RunDuration = time.Since(r.Timestamp).Seconds()
	}()

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	var exitErr *exec.ExitError
	r.Err = cmd.Run()
	if r.Err != nil && !errors.As(r.Err, &exitErr) {
		r.Err = fmt.Errorf("failed to exec command: %w", r.Err)
		r.Stderr = r.Err.Error()
		return
	}
	if cmd.Process != nil {
		r.Pid = cmd.Process.Pid
	}
	if cmd.ProcessState != nil {
		r.Rusage = cmd.ProcessState.SysUsage().(*syscall.Rusage)
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	r.Stdout = stdout.Bytes()
	r.Stderr = stderr.String()

	if r.ExitCode != 0 {
		r.Err = fmt.Errorf("failed with exit-code %d\n%s\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	return
}
