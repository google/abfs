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

package debug

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
	on      bool
	off     bool
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd}
}
func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "debug",
		Usage: "[PATH]...",
		Desc: `Read the debug attribute of the given PATHs (the current directory by default).

Turn on/off debug for the input PATH(s).  Debug messages can be read via dmesg.  Turning on debug
can adversely effect performance of the casfs.
`,
	}
}
func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.on, "on", "", false, "Turn debug on for the input PATH(s)")
	fl.BoolVarP(&c.off, "off", "", false, "Turn debug off for the input PATH(s)")
}

func (c *cmd) run(path string) error {
	var err error
	if c.on {
		err = c.rootCmd.FS().SetDebug(path, true)
	} else if c.off {
		err = c.rootCmd.FS().SetDebug(path, false)
	} else {
		var on bool
		on, err = c.rootCmd.FS().Debug(path)
		if err != nil {
			return err
		}
		if on {
			fmt.Println(path + ":on")
		} else {
			fmt.Println(path + ":off")
		}
	}
	return nil
}
func (c *cmd) Run(fl *pflag.FlagSet) {
	if c.on && c.off {
		log.Fatal("cannot setup both debug on and off")
	}
	failed := false
	for _, l := range args.ArgsWithCWDDefault(fl) {
		if err := c.run(l); err != nil {
			fmt.Println(err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}

}
