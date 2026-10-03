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

package repo

import (
	"context"
	"log"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type repoCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	*cmd.CachemanProvider

	client *binding.Client
	casfs  bindcas.CASFS
	casgit *casgit.CASGit
}

func NewCmd(rootCmd *root.RootCmd) *repoCmd {
	return &repoCmd{
		RootCmd: rootCmd,
	}
}

func (c *repoCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "repo",
		Usage:       "<command> <args>...",
		Desc:        `Commands for managing workspaces`,
		Subcommands: c.subcommands(),
	}
}

func (c *repoCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *repoCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		newInitCmd(c),
		newSyncCmd(c),
	}
}

func (c *repoCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return clierr.NoSubcommandErr(fl)
}

func (c *repoCmd) setupCAS() (func(), error) {
	client, err := binding.NewClient(".", entities.ABFSSocketFileName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return nil, clierr.CLIErr("Current directory is not on a CASFS mount.")
	}
	c.client = client
	c.casgit = casgit.NewWithClient(client, c.Cacheman)
	c.casfs = bindcas.NewExternalCASFS(c.client.Root())
	return func() {
		err := client.Close()
		if err != nil {
			log.Printf("Failed to close client: %v", err)
		}
	}, nil
}
