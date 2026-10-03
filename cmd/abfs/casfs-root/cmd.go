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

package casfsroot

import (
	"context"
	"fmt"
	_ "net/http/pprof"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
)

type casfsRootCmd struct {
	*root.RootCmd
}

func NewCmd(rootCmd *root.RootCmd) *casfsRootCmd {
	return &casfsRootCmd{RootCmd: rootCmd}
}

func (c *casfsRootCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "casfs-root",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Find the root of the casfs mount.`,
		Hidden:  true,
	}
}

func (c *casfsRootCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *casfsRootCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	root, err := c.run()
	if err != nil {
		return clierr.CLIErr("Failed to get hash. Is this directory inside a casfs mount? err: %v", err)
	}

	fmt.Println(root)
	return nil
}

func (c *casfsRootCmd) run() (string, error) {
	if _, err := bindcas.Hash("."); err != nil {
		return "", err
	}
	return cmd.MountPoint(".")
}
