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

package hash

import (
	"fmt"
	"log"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cmd/args"
	"github.com/google/abfs/cas/cmd/fs/root"
)

type cmd struct {
	rootCmd *root.RootCommand
	hash    string
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}

func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.hash, "hash", "h", "", fmt.Sprintf("Set the path hash to input string (%s)", c.rootCmd.HasherDescription()))
}

func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "hash",
		Usage: "[-h HASH] [PATH]",
		Desc: `Read (or set) the hash value of the given PATH.

Read the hash value of the given input path.
If hash option is given set the hash of the path, this will cause the path to contain the
data associated with the input path.`}
}

func (c *cmd) Run(fl *pflag.FlagSet) {
	path := args.EnsureSingleArg(fl)

	if c.hash == "" {
		h, err := c.rootCmd.FS().Hash(path)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(h)
	} else {
		h, err := c.rootCmd.Hasher.NewHash(c.hash)
		if err != nil {
			log.Fatal(err)
		}
		if err = c.rootCmd.FS().SetHash(path, h); err != nil {
			log.Fatal(err)
		}
	}
}
