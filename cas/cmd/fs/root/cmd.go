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

package root

import (
	"path/filepath"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/fs/xattr"
	"github.com/google/abfs/cas/hash/hashers"
)

// Only one hasher currently supported
const hasherType = hashers.HasherTypeSHA1

type RootCommand struct {
	Commands   []cli.Command
	flagSet    *pflag.FlagSet
	Hasher     fs.Hasher
	fs         fs.FS
	MountPoint string
}

func NewRootCommand() *RootCommand {
	return &RootCommand{
		Hasher: hashers.CreateHasher(hasherType),
	}
}
func (c *RootCommand) HasherDescription() string {
	return hasherType.String()
}
func (c *RootCommand) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVar(&c.MountPoint, "mount-point", "", "casfs mount point")
}
func (c *RootCommand) Run(fl *pflag.FlagSet) {
	fl.Usage()
}

func (c *RootCommand) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:  "casfs",
		Usage: "<command>",
		Desc:  `Command line interface for casfs api.`,
	}
}
func (c *RootCommand) Subcommands() []cli.Command {
	return c.Commands
}
func (c *RootCommand) FS() fs.FS {
	if c.fs == nil {
		c.fs = xattr.NewCASFS(c.MountPoint, c.Hasher)
	}
	return c.fs
}

// TODO: this is specific to the xattr api implementation.
func (c *RootCommand) SetTapPath(tapPath string) {
	xattr.SetTapPath(c.FS(), filepath.Join(c.MountPoint, tapPath))
}
