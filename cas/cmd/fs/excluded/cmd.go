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

package excluded

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
	exc     bool
	inc     bool
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}
func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "excluded",
		Usage: "[-x] [-i] [PATH]...",
		Desc: `Read or write the excluded attribute of the given PATHs (the current directory by default).

Files and directories can be excluded from inclusion in their parent's directory hash.
`,
	}
}
func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.exc, "exclude", "x", false, "Set path to be excluded from parent's hash")
	fl.BoolVarP(&c.inc, "include", "i", false, "Set path to be included from parent's hash")
}

func (c *cmd) run(path string) error {
	var err error
	if c.exc {
		err = c.rootCmd.FS().SetExcluded(path, true)
	} else if c.inc {
		err = c.rootCmd.FS().SetExcluded(path, false)
	} else {
		var excluded bool
		excluded, err = c.rootCmd.FS().Excluded(path)
		if err != nil {
			return err
		}
		if excluded {
			fmt.Println(path + ":excluded")
		} else {
			fmt.Println(path + ":included")
		}
	}
	return err
}
func (c *cmd) Run(fl *pflag.FlagSet) {
	if c.exc && c.inc {
		log.Fatal("cannot set both excluded and included")
	}
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
