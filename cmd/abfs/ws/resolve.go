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

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
)

type resolveCmd struct {
	*wsCmd
	paths []string
}

func newResolveCmd(c *wsCmd) *resolveCmd {
	return &resolveCmd{wsCmd: c}
}

func (c *resolveCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:   "resolve",
		Usage:  "<path>...",
		Desc:   `Resolve the files with conflicts.`,
		Hidden: true,
	}
}

func (c *resolveCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *resolveCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.initWorkspaces()

	c.paths = args.EnsureArgs(fl)

	return c.exec()
}

func (c *resolveCmd) exec() error {
	return c.workspaces.Resolve(c.paths)
}
