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

package systemd

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
)

type TailCmd struct {
	*root.RootCmd
	Servicer Servicer
	all      bool
}

func NewTailCmd(rootCmd *root.RootCmd, servicer Servicer) *TailCmd {
	return &TailCmd{RootCmd: rootCmd, Servicer: servicer}
}

func (c *TailCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:            "tail",
		Usage:           "[--all]",
		Aliases:         []string{},
		Desc:            fmt.Sprintf("Tail the %s log.", c.Servicer.Name()),
		ProviderInjects: []any{c.Servicer},
	}
}

func (c *TailCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.all, "all", "a", false, "When true, tail the entire file(s) without following")
}

func (c *TailCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	stdoutFile := c.Servicer.Service().StdoutFile
	logFile := c.Servicer.Service().LogFile
	var args []string
	if c.all {
		args = append(args, "-n", "+1")
	} else {
		args = append(args, "-F")
	}
	args = append(args, logFile)
	if stdoutFile != "" && stdoutFile != logFile {
		args = append(args, stdoutFile)
	}
	cmd := exec.CommandContext(ctx, "tail", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
