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

package testclient

import (
	"context"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
)

type testClientCmd struct {
	*root.RootCmd
	*cmd.ABFSClientProvider
}

func NewCmd(rootCmd *root.RootCmd) *testClientCmd {
	return &testClientCmd{RootCmd: rootCmd}
}

func (c *testClientCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "test-client",
		Aliases:     []string{},
		Desc:        `Connect to an ABFS server and perform basic requests`,
		Subcommands: c.subcommands(),
	}
}

func (c *testClientCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		NewHeadersCmd(c),
		NewGetObjectCmd(c),
		NewPutObjectCmd(c),
		NewAddMapResultCmd(c),
		NewMapResultsCmd(c),
		NewPutRefCmd(c),
		NewGetRefCmd(c),
		NewSearchRefCmd(c),
		NewHealthCheckCmd(c),
		NewWhoAmICmd(c),
	}
}

func (c *testClientCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *testClientCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return clierr.NoSubcommandErr(fl)
}
