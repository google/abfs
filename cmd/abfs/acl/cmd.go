// Copyright 2025 Google LLC
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

package acl

import (
	"context"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
)

type aclCmd struct {
	*root.RootCmd
	server  string
	project string
	path    string
}

func NewCmd(rootCmd *root.RootCmd) *aclCmd {
	return &aclCmd{RootCmd: rootCmd}
}

func (c *aclCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "acl",
		Aliases:     []string{},
		Usage:       "", //TODO: add usage
		Hidden:      true,
		Desc:        `Access Control List management`,
		Subcommands: c.subcommands(),
	}
}

func (c *aclCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		NewShowCmd(c),
		NewUpdateCmd(c),
	}
}

func (c *aclCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.server, "server", "s", "", "The server for the ACL ref")
	fl.StringVarP(&c.project, "project", "p", "", "The project for the ACL ref")
	fl.StringVarP(&c.path, "path", "", "", "The path to the ACL file")
}

func (c *aclCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return clierr.NoSubcommandErr(fl)
}
