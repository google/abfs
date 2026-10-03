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

package git

import (
	"context"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
)

type gitCmd struct {
	*root.RootCmd
}

func NewCmd(rootCmd *root.RootCmd) *gitCmd {
	return &gitCmd{RootCmd: rootCmd}
}

func (c *gitCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "git",
		Usage:       "",
		Aliases:     []string{},
		Desc:        "Collection of tools that might help debug or speed up git in ABFS manner.",
		Subcommands: c.subcommands(),
	}
}

func (c *gitCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *gitCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		NewSetIndexCmd(c),
		NewCloneCmd(c),
		NewFetchCmd(c),
		NewCheckoutCmd(c),
	}
}

func (c *gitCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return clierr.NoSubcommandErr(fl)
}
