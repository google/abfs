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

package main

import (
	"fmt"
	"os"
	"path"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/shared/version"
)

const abfsbox = "abfsbox"

func cmdArgs(args []string) []string {
	name := path.Base(args[0])
	if name == abfsbox {
		return args[1:]
	}
	return append([]string{name}, args[1:]...)
}

type Command struct {
	showVersion bool
}

func (c *Command) Run(fl *pflag.FlagSet) {
	if c.showVersion {
		fmt.Println(version.Version())
		return
	}
	fl.Usage()
}

func (c *Command) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  abfsbox,
		Usage: "<command> [flags]",
		Desc:  `This is a busybox-like client for the abfs tools used while doing a build.`,
	}
}

func (c *Command) Subcommands() []cli.Command {
	return []cli.Command{
		NewBuildStartedCmd(),
		NewBuildFinishedCmd(),
		NewCopyCmd(),
		NewHashCmd(),
		NewCachemanConfigCmd(),
	}
}
func (c *Command) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.showVersion, "version", false, "Print the application version")
}

func main() {
	args := cmdArgs(os.Args)
	for _, arg := range args {
		if arg == "--version" || arg == "-version" {
			fmt.Println(version.Version())
			return
		}
	}
	root := new(Command)
	cli.Run(root, args, "")
}
