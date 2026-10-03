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

package gitclone

import (
	"context"
	"log"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type gitClone struct {
	*root.RootCmd
	*cmd.ConfigProvider
	path       string
	socketName string
	root       string
	commit     string
}

func NewCmd(rootCmd *root.RootCmd) *gitClone {
	return &gitClone{RootCmd: rootCmd}
}

func (c *gitClone) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "git-clone",
		Usage:   "-p <dest path> -s <mount socket> -c <commit revision>",
		Aliases: []string{},
		Desc:    `Create a git repository in a casfs mount`,
	}
}

func (c *gitClone) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.path, "path", "p", "", "Relative destination path inside mount")
	fl.StringVarP(&c.root, "root", "r", ".", "The abfs root directory")
	fl.StringVarP(&c.socketName, "socket", "s", entities.ABFSSocketFileName, "Mount socket path")
	fl.StringVarP(&c.commit, "commit", "c", "", "Commit revision")
}

func (c *gitClone) checkArgs() error {
	if c.path == "" {
		return clierr.CLIErr("path was not provided")
	}

	if c.commit == "" {
		return clierr.CLIErr("commit was not provided")
	}

	return nil
}

func (c *gitClone) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if err := c.checkArgs(); err != nil {
		return err
	}

	client, err := binding.NewClient(c.root, c.socketName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return err
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("error closing client: %v", err)
		}
	}()

	return client.Clone(c.commit, c.path)
}
