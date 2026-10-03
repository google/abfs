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
	"log"
	"os"
	"path"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

type fetchCmd struct {
	*gitCmd
	*cmd.ConfigProvider
	*cmd.CachemanProvider

	casgit.FetchOptions
}

func NewFetchCmd(gitCmd *gitCmd) *fetchCmd {
	return &fetchCmd{gitCmd: gitCmd}
}

func (c *fetchCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "fetch",
		Usage: "[<options>] [<repository> [<refspec>...]]",
		Desc:  `Fetch branches and/or tags from one or more other repositories`,
	}
}

func (c *fetchCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.All, "all", false, "fetch from all remotes")
	fl.BoolVarP(&c.Prune, "prune", "p", false, "prune remote-tracking branches no longer on remote")
	fl.BoolVarP(&c.NoTags, "no-tags", "n", false, "do not fetch tags")
	fl.BoolVarP(&c.Tags, "tags", "t", false, "fetch all tags")
	fl.BoolVarP(&c.Quiet, "quiet", "q", false, "operate quietly")
	fl.BoolVarP(&c.Verbose, "verbose", "v", false, "be verbose")
}

func (c *fetchCmd) checkArgs(fl *pflag.FlagSet) error {
	if fl.NArg() > 0 {
		c.Remote = fl.Arg(0)
		c.RefSpecs = fl.Args()[1:]
	}
	return nil
}

func (c *fetchCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if err := c.checkArgs(fl); err != nil {
		return clierr.CLIUsage(fl, "fatal: %v", err)
	}

	return c.run(ctx)
}

func (c *fetchCmd) run(ctx context.Context) error {
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}

	gitDir, err := casgit.FindGitDir(pwd)
	if err != nil {
		return err
	}

	client, err := binding.NewClient(gitDir, entities.ABFSSocketFileName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return err
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("Failed to close client: %v", err)
		}
	}()

	casgitObj := casgit.NewWithClient(client, c.Cacheman)

	if c.GitDir == "" {
		c.GitDir = gitDir
	}
	if c.ObjectDir == "" {
		c.ObjectDir = path.Join(c.GitDir, "objects")
	}

	return casgitObj.Fetch(ctx, c.FetchOptions)
}
