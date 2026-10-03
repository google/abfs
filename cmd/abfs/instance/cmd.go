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

package instance

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
)

type instanceCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
}

func NewCmd(rootCmd *root.RootCmd) *instanceCmd {
	return &instanceCmd{RootCmd: rootCmd}
}

func (c *instanceCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "instance",
		Usage:       "",
		Aliases:     []string{},
		Desc:        "Manage ABFS instances.",
		Subcommands: c.subcommands(),
	}
}

func (c *instanceCmd) RegisterFlags(fl *pflag.FlagSet) {}

func (c *instanceCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		NewListCmd(c),
		NewCreateCmd(c),
		NewRemoveCmd(c),
		NewRenameCmd(c),
	}
}

func (c *instanceCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	fmt.Println(c.Instance.Name)
	return nil
}
