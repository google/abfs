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

package mountclient

import (
	"context"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
)

type mountClientCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
}

func NewCmd(rootCmd *root.RootCmd) *mountClientCmd {
	return &mountClientCmd{RootCmd: rootCmd}
}

func (c *mountClientCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "mount-client",
		Aliases:     []string{},
		Usage:       "",
		Desc:        `Mount client utilities`,
		Subcommands: c.subcommands(),
	}
}

func (c *mountClientCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		newProviderPingCmd(c),
	}
}

func (c *mountClientCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *mountClientCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return clierr.NoSubcommandErr(fl)
}
