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

package dirlist

import (
	"fmt"
	"log"
	"os"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cmd/args"
	"github.com/google/abfs/cas/cmd/fs/root"
)

type cmd struct {
	rootCmd *root.RootCommand
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}
func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "dirlist",
		Usage: "[PATH]...",
		Desc: `Read the directory list hash attribute of the given PATHs (the current directory by default).

Get the directory list hash of the input PATH(s).
`,
	}
}
func (c *cmd) run(path string) error {
	h, err := c.rootCmd.FS().DirList(path)
	if err != nil {
		return err
	}
	fmt.Println(path + ":" + h.String())
	return nil
}
func (c *cmd) Run(fl *pflag.FlagSet) {
	failed := false
	for _, l := range args.ArgsWithCWDDefault(fl) {
		if err := c.run(l); err != nil {
			log.Println(err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}

}
