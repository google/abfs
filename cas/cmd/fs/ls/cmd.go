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

package ls

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

	recursive bool
	noColor   bool
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{
		rootCmd: rootCmd,
	}
}

func (c *cmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "ls",
		Usage: "[OPTION]... [FILE]...",
		Desc: `List information about the FILEs (the current directory by default).


List casfs information about the FILEs.
Information includes loaded, dirty, and hash attribute of the FILEs.
`,
	}
}

func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.noColor, "no-color", false, "Do not use color")
	fl.BoolVarP(&c.recursive, "recursive", "R", false, "Enable long mode")
}

func (c *cmd) Run(fl *pflag.FlagSet) {

	o, _ := os.Stdout.Stat()
	if (o.Mode() & os.ModeCharDevice) != os.ModeCharDevice {
		c.noColor = true
	}

	failed := false
	for _, l := range args.ArgsWithCWDDefault(fl) {
		info, err := os.Lstat(l)
		if err != nil {
			fmt.Println(err)
			failed = true
			continue
		}
		if info.IsDir() {
			c.listDir(l)
		} else {
			c.lsEntry(l, info)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// ls for single entry return filtered status
func (c *cmd) lsEntry(path string, info fs.FileInfo) {

	loadedStr := "X"
	dirtyStr := "X"
	loaded, err := c.rootCmd.FS().Loaded(path)
	if err == nil {
		if loaded {
			loadedStr = "1"
		} else {
			loadedStr = "0"
		}
	}
	hash, err := c.rootCmd.FS().Hash(path)
	if err != nil {
		hash = c.rootCmd.Hasher.ZeroHash()
	}
	dirty, err := c.rootCmd.FS().Dirty(path)
	if err == nil {
		if dirty {
			dirtyStr = "1"
		} else {
			dirtyStr = "0"
		}
	}

	color := ""
	if !c.noColor {
		switch loadedStr {
		case "X":
			color = Purple
			break
		case "1":
			color = c.osModeToColor(info.Mode())
			break
		default:
			color = Gray
			break
		}
	}

	fmt.Printf("%s %s %s %s  %s%s%s\n",
		loadedStr, dirtyStr, info.Mode(), hash, color, path, Reset)
}
func (c *cmd) listDir(dir string) error {

	filepath.Walk(dir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		c.lsEntry(path, info)

		if info.Mode().IsDir() && (path != dir) {
			if !c.recursive {
				return filepath.SkipDir
			}
		}

		return nil

	})
	return nil
}

var (
	Reset  = "\033[0m"
	Red    = "\033[1;31m"
	Green  = "\033[1;32m"
	Yellow = "\033[1;33m"
	Blue   = "\033[1;34m"
	Purple = "\033[1;35m"
	Cyan   = "\033[1;36m"
	Gray   = "\033[1;37m"
	White  = "\033[1;97m"
)

func (c *cmd) osModeToColor(m fs.FileMode) string {
	if c.noColor {
		return ""
	}

	if m.IsDir() {
		return Blue
	}
	if m&fs.ModeSymlink == fs.ModeSymlink {
		return Cyan
	}

	if m.IsRegular() {
		if m&0111 != 0 {
			return Green
		}
		return ""
	}
	return Red

}
