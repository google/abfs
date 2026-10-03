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

package deeptree

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cmd/root"
	"github.com/spf13/pflag"
	"go.coder.com/cli"
)

type cmd struct {
	rootCmd  *root.RootCommand
	out      io.Writer
	maxDepth int
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd,
		out: os.Stdout}
}
func (c *cmd) Spec() cli.CommandSpec {
	desc := `Create a deeptree from a given input directory hash.

Hash string format: `
	return cli.CommandSpec{
		Name:  "deeptree",
		Usage: "[-d count] HASH",
		Desc:  desc + c.rootCmd.HasherDescription(),
	}
}
func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.IntVarP(&c.maxDepth, "max-depth", "d", 256, "Max depth of deeptree")
}

func (c *cmd) Run(fl *pflag.FlagSet) {

	h, store := c.rootCmd.HashAndStoreFromSingleArg(fl)

	dh, err := cas.CreateDeepTree(h, store, int64(c.maxDepth))
	if err != nil {
		log.Fatal(h)
	}
	fmt.Printf("New deeptree %s\n", dh)

}
