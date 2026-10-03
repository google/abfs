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

package git

import (
	"log"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cmd/args"
	"github.com/google/abfs/cas/cmd/fs/root"
)

type cmd struct {
	rootCmd *root.RootCommand
	dir     string
	head    string
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}

func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.dir, "dir", "d", ".git", "Name of top level git directory")
	fl.StringVar(&c.head, "head", "", "HEAD value")
}
func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "git",
		Usage: "[-d directory] [-h HEAD] [DIR]",
		Desc: `Create GIT project under the given input DIR.
`,
	}
}

func (c *cmd) Run(fl *pflag.FlagSet) {
	if c.dir == "" {
		log.Fatal("directory name not specified")
	}
	path := args.EnsureSingleArg(fl)
	err := c.rootCmd.FS().CreateGit(path, c.dir, c.head)
	if err != nil {
		log.Fatal(err)
	}
}
