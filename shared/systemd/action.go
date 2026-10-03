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
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
)

type ActionCmd struct {
	*root.RootCmd
	Servicer Servicer
	Action   string
}

func NewActionCmd(rootCmd *root.RootCmd, servicer Servicer, action string) *ActionCmd {
	return &ActionCmd{RootCmd: rootCmd, Servicer: servicer, Action: action}
}

func (c *ActionCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:            c.Action,
		Usage:           "",
		Aliases:         []string{},
		Desc:            fmt.Sprintf("%s %s service.", cases.Title(language.English).String(c.Action), c.Servicer.Name()),
		ProviderInjects: []any{c.Servicer},
	}
}

func (c *ActionCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *ActionCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	if err := c.run(ctx); err != nil {
		return err
	}

	fmt.Printf("%s %s done.\n", cases.Title(language.English).String(c.Action), c.Servicer.Service().Name)
	return nil
}

func (c *ActionCmd) run(ctx context.Context) error {
	if c.Action == "restart" {
		cmd := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}

	if c.Action == "disable" {
		cmd := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reexec")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}

	service := c.Servicer.Service()
	if c.Action == "remove" {
		if err := service.RunAction(ctx, "stop"); err != nil {
			return err
		}

		if err := service.RunAction(ctx, "disable"); err != nil {
			return err
		}

		serviceFile := fmt.Sprintf("%s/.config/systemd/user/%s.service", os.ExpandEnv("$HOME"), c.Servicer.Service().Name)
		return os.Remove(serviceFile)
	}

	if err := service.RunAction(ctx, c.Action); err != nil {
		return err
	}

	return nil
}
