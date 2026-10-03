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

package ares

import (
	"context"
	_ "net/http/pprof"

	"github.com/spf13/pflag"

	testclient "github.com/google/abfs/cmd/abfs/ares/test-client"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
)

type ARESCmd struct {
	*root.RootCmd
}

func NewCmd(rootCmd *root.RootCmd) *ARESCmd {
	return &ARESCmd{RootCmd: rootCmd}
}

func (c *ARESCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "ares",
		Usage:       "",
		Aliases:     []string{},
		Desc:        `A set of commands for remote building with abfs.`,
		Subcommands: c.subcommands(),
	}
}

func (c *ARESCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *ARESCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		NewServerCmd(c),
		testclient.NewARESTestClientCmd(c.RootCmd),
		NewCreatePatchCmd(c),
	}
}

func (c *ARESCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return clierr.NoSubcommandErr(fl)
}
