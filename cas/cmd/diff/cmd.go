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

package diff

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cmd/args"
	"github.com/google/abfs/cas/cmd/root"
)

type cmd struct {
	rootCmd     *root.RootCommand
	out         io.Writer
	showParents bool
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd,
		out: os.Stdout}
}
func (c *cmd) Spec() cli.CommandSpec {
	desc := `Diff two hashes

Hash string format: `
	return cli.CommandSpec{
		Name:  "diff",
		Usage: " HASH-1 HASH-2",
		Desc:  desc + c.rootCmd.HasherDescription(),
	}
}
func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.showParents, "show-parents", "p", false, "Show the parent directory changes as well.")
}

func (c *cmd) Run(fl *pflag.FlagSet) {

	printer := func(action cas.DiffAction, dir string, de *cas.DirEntry) {
		fmt.Fprintf(c.out, "%s %d %s %s\n", action, de.Mode, de.Hash, filepath.Join(dir, de.Name))
	}
	hashStr1, hashStr2 := args.EnsureTwoArgs(fl)

	store, err := c.rootCmd.Store()
	if err != nil {
		log.Fatal(err)
	}

	h1, err := c.rootCmd.CreateHash(hashStr1)
	if err != nil {
		log.Fatal(err)
	}
	h2, err := c.rootCmd.CreateHash(hashStr2)
	if err != nil {
		log.Fatal(err)
	}

	err = cas.Diff(h1, h2, store, c.showParents, printer)
	if err != nil {
		log.Fatalf("%v", err)
	}

}
