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

package find

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cmd/args"
	"github.com/google/abfs/cas/cmd/fs/root"
)

type cmd struct {
	rootCmd *root.RootCommand

	fsFilter args.FSTypes
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{
		rootCmd: rootCmd,
	}
}

func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "find",
		Usage: "[OPTION]... [FILE]...",
		Desc: `Search for files in a directory hierarchy (the current directory by default).


Find loaded files in a casfs file system of given input type.
This command can be used to feed other tools (grep,cscope) to search only the local files
to prevent downloading the content of unloaded files.
`,
	}
}

func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.VarP(&c.fsFilter, "type", "t", fmt.Sprintf("File is of type (%s)", args.FSAllTypes()))
}

func (c *cmd) Run(fl *pflag.FlagSet) {

	failed := false
	for _, l := range args.ArgsWithCWDDefault(fl) {
		info, err := os.Lstat(l)
		if err == nil {
			if info.IsDir() {
				err = c.dir(l)
			} else {
				_, err = c.entry(l, info)

			}
		}
		if err != nil {
			fmt.Println(err)
			failed = true
			continue
		}

	}
	if failed {
		os.Exit(1)
	}
}

// handle entry
func (c *cmd) entry(path string, info fs.FileInfo) (bool, error) {

	loaded, err := c.rootCmd.FS().Loaded(path)
	if err != nil {
		return false, err
	}
	if !loaded {
		return false, nil
	}
	applyFilter := len(c.fsFilter) != 0
	match := false
	for _, filter := range c.fsFilter {
		switch filter {
		case args.FSTypeRegular:
			if info.Mode().IsRegular() {
				if info.Mode()&0111 == 0 {
					match = true
				}

			}
			break
		case args.FSTypeExecutable:
			if info.Mode().IsRegular() {
				if info.Mode()&0111 != 0 {
					match = true
				}
			}
			break
		case args.FSTypeDirectory:
			if info.Mode().IsDir() {
				match = true
			}
			break
		case args.FSTypeDirty:
			dirty, err := c.rootCmd.FS().Dirty(path)
			if err != nil {
				return false, err
			}
			match = dirty
		}
	}
	if !applyFilter || match {
		fmt.Printf("%s\n", path)
	}
	return true, nil
}

// Walk directory
func (c *cmd) dir(dir string) error {

	filepath.Walk(dir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Ignore error as control entries have no loaded state
		loaded, _ := c.entry(path, info)
		if info.Mode().IsDir() && (path != dir) {
			if !loaded {
				return filepath.SkipDir
			}
		}

		return nil

	})
	return nil
}
