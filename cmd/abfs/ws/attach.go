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
	"log"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

type attachCmd struct {
	*wsCmd
	workDir     string
	wsName      string
	strategyStr string
	newWS       bool
}

func newAttachCmd(c *wsCmd) *attachCmd {
	return &attachCmd{wsCmd: c}
}

func (c *attachCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "attach",
		Usage:   "<ws-name> [<work-dir>] [-s <strategy>] [-n]",
		Aliases: []string{},
		Desc:    `Attach an existing workspace to the specified work-dir.`,
		Hidden:  true,
	}
}

func (c *attachCmd) RegisterFlags(fl *pflag.FlagSet) {
	args.OptChoice(fl, &c.strategyStr, "strategy", "s", string(binding.PutStrategyExactMatch), []string{"exact-match", "local-wins", "remote-wins"}, "Strategy to use for the workspace (exact-match, local-wins, remote-wins)")
	fl.BoolVarP(&c.newWS, "new", "n", false, "Create a new workspace")
}

func (c *attachCmd) validateArgs(fl *pflag.FlagSet) error {
	switch len(fl.Args()) {
	case 0:
		return clierr.CLIUsage(fl, "No arguments specified.")
	case 1:
		c.wsName = fl.Arg(0)
		c.workDir = "."
	case 2:
		c.wsName = fl.Arg(0)
		c.workDir = fl.Arg(1)
	default:
		return clierr.CLIUsage(fl, "Too many arguments.")
	}
	if c.strategyStr != "" {
		if !binding.PutStrategy(c.strategyStr).IsValid() {
			return clierr.CLIErr("Invalid strategy: %s", c.strategyStr)
		}
	}
	return nil
}

func (c *attachCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.initWorkspaces()

	if err := c.validateArgs(fl); err != nil {
		return err
	}

	abfs, err := binding.NewClient(c.workDir, entities.ABFSSocketFileName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return err
	}
	defer func() {
		if err := abfs.Close(); err != nil {
			log.Printf("Error closing abfs client: %v", err)
		}
	}()

	return abfs.PutWS(c.wsName, c.newWS, binding.PutStrategy(c.strategyStr))
}
