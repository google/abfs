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

package resolve

import (
	"fmt"
	"log"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cmd/args"
	"github.com/google/abfs/cas/cmd/root"
)

type cmd struct {
	rootCmd *root.RootCommand
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}
func (c *cmd) Spec() cli.CommandSpec {
	desc := `Resolve a path against a given HASH, symlinks are followed to find the entry.

Hash string format: `
	return cli.CommandSpec{
		Name:  "resolve",
		Usage: " HASH PATH",
		Desc:  desc + c.rootCmd.HasherDescription(),
	}
}

func (c *cmd) Run(fl *pflag.FlagSet) {

	hashStr, p := args.EnsureTwoArgs(fl)

	store, err := c.rootCmd.Store()
	if err != nil {
		log.Fatal(err)
	}

	h, err := c.rootCmd.CreateHash(hashStr)
	if err != nil {
		log.Fatal(err)
	}

	e, err := cas.Resolve(store, h, p)
	if err != nil {
		log.Fatalf("failed to resolve %s: %v", p, err)
	}

	fmt.Println(*e)
}
