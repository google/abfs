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

package hashsize

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
	size    int64
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}

func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.hash, "hash", "h", "", fmt.Sprintf("Set the path hash to input string (%s)", c.rootCmd.HasherDescription()))
	fl.Int64VarP(&c.size, "size", "s", 0, "Set the file size")
}

func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "hashsize",
		Usage: "[-h HASH] [-s SIZE] [FILE]",
		Desc: `Read (or set) the hash and size of the given FILE.

Read the hash value and size of the given input file.
If set option is given set the hash and size of the input PATH.

By setting a file's HASH and size the file can be stat'd without having to resolve
the content of the file.
`,
	}
}

func (c *cmd) Run(fl *pflag.FlagSet) {
	path := args.EnsureSingleArg(fl)

	if c.hash == "" {
		h, sz, err := c.rootCmd.FS().HashSize(path)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s:%d\n", h, sz)
	} else {
		h, err := c.rootCmd.Hasher.NewHash(c.hash)
		if err != nil {
			log.Fatal(err)
		}
		if err = c.rootCmd.FS().SetHashSize(path, h, c.size); err != nil {
			log.Fatal(err)
		}
	}
}
