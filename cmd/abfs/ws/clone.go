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

package ws

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
)

type cloneCmd struct {
	*wsCmd
	wsName string
	wsNew  string
}

func newCloneCmd(c *wsCmd) *cloneCmd {
	return &cloneCmd{wsCmd: c}
}

func (c *cloneCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "clone",
		Usage: "<ws-name> <new-ws>",
		Desc:  `Clone a workspace to a new one`,
	}
}

func (c *cloneCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *cloneCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.initWorkspaces()

	c.wsName, c.wsNew = args.EnsureTwoArgs(fl)

	return c.exec()
}

func (c *cloneCmd) exec() error {
	if err := c.workspaces.Clone(c.wsName, c.wsNew); err != nil {
		return err
	}
	fmt.Printf("Successfully cloned workspace %s to %s.\n", c.wsName, c.wsNew)
	return nil
}
