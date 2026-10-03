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

package hashes

import (
	"fmt"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cmd/root"
)

type cmd struct {
	rootCmd *root.RootCommand
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}
func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "hashes",
		Usage: "",
		Desc:  `Display the constant hashes.`,
	}
}

func (c *cmd) Run(fl *pflag.FlagSet) {
	fmt.Printf("empty dir  : %s\n", c.rootCmd.Hasher.EmptyDirHash())
	fmt.Printf("empty file : %s\n", c.rootCmd.Hasher.EmptyFileHash())
	fmt.Printf("zero       : %s\n", c.rootCmd.Hasher.ZeroHash())
}
