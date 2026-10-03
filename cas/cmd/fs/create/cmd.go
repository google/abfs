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

package create

import (
	"log"
	"os"
	"path"
	"syscall"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/cmd/args"
	"github.com/google/abfs/cas/cmd/fs/root"
)

type cmd struct {
	rootCmd *root.RootCommand
	dir     bool
	exec    bool
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{
		rootCmd: rootCmd,
	}
}

func (c *cmd) Spec() cli.CommandSpec {
	desc := `Create a new file (or directory) based on the input hash value.
Create a new regular file or directory (-d) with the input hash value.  If the input PATH
exists this command will fail.  When creating a regular file the file can be created as
an executable via the -x option.

Hash string format: `

	return cli.CommandSpec{
		Name:  "create",
		Usage: " [-d] [-x] HASH PATH",
		Desc:  desc + c.rootCmd.HasherDescription(),
	}
}
func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.dir, "dir", "d", false, "Create directory")
	fl.BoolVarP(&c.exec, "executable", "x", false, "Create file as executable")
}
func (c *cmd) Run(fl *pflag.FlagSet) {
	hashStr, p := args.EnsureTwoArgs(fl)

	hash, err := c.rootCmd.Hasher.NewHash(hashStr)
	if err != nil {
		log.Fatalf("create: invalid hash '%s'", hashStr)
	}
	if _, err := os.Lstat(p); err == nil {
		log.Fatalf("create: error path '%s' exists", p)
	}

	if c.dir {
		if err := os.MkdirAll(p, 0755); err != nil {
			log.Fatalf("create: failed to create directory '%s'", p)
		}
	} else {
		if err := os.MkdirAll(path.Dir(p), 0755); err != nil {
			log.Fatalf("create: failed to create parent directory for '%s'", p)
		}
		osMode := os.FileMode(0644)
		if c.exec {
			osMode = os.FileMode(0755)
		}
		if err := syscall.Mknod(p, syscall.S_IFREG|uint32(osMode), 0); err != nil {
			log.Fatalf("create: failed to mknod file '%s' : %v", p, err)
		}
	}
	if err := c.rootCmd.FS().SetHash(p, hash); err != nil {
		log.Fatalf("create: failed to set xattr for '%s' : %v", p, err)
	}
}
