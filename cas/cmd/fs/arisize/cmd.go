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

package arisize

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
	ari     string
	size    int64
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}

func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.ari, "ari", "a", "", "Set the path ARI to input string")
	fl.Int64VarP(&c.size, "size", "s", 0, "Set the file size")
}

func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "arisize",
		Usage: "[-a ARI] [-s SIZE] [FILE]",
		Desc: `Read (or set) the ARI and size of the given FILE.

Read the ari value and size of the given input file.
If set option is given set the ari and size of the input PATH.

By setting a file's ARI and size the file can be stat'd without having to resolve
the content of the file.
`,
	}
}

func (c *cmd) Run(fl *pflag.FlagSet) {
	path := args.EnsureSingleArg(fl)

	if c.ari == "" {
		h, sz, err := c.rootCmd.FS().ARISize(path)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s:%d\n", h, sz)
	} else {
		if err := c.rootCmd.FS().SetARISize(path, c.ari, c.size); err != nil {
			log.Fatal(err)
		}
	}
}
