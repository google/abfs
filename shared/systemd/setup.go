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

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
)

type SetupCmd struct {
	*root.RootCmd
	Servicer Servicer
}

func NewSetupCmd(rootCmd *root.RootCmd, servicer Servicer) *SetupCmd {
	return &SetupCmd{RootCmd: rootCmd, Servicer: servicer}
}

func (c *SetupCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:            "setup",
		Usage:           "",
		Aliases:         []string{},
		Desc:            fmt.Sprintf("Setup %s service.", c.Servicer.Name()),
		ProviderInjects: []any{c.Servicer},
	}
}

func (c *SetupCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *SetupCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	service := c.Servicer.Service()
	if err := service.Setup(ctx); err != nil {
		return err
	}

	fmt.Printf("%s set up and started\n", service.Name)
	return nil
}
