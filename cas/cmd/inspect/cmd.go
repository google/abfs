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

package inspect

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cmd/root"
	"github.com/google/abfs/cas/hash"

	"github.com/spf13/pflag"
	"go.coder.com/cli"
)

type cmd struct {
	rootCmd   *root.RootCommand
	out       io.Writer
	recursive bool
	verbose   bool
}

func NewCmd(rootCmd *root.RootCommand) *cmd {
	return &cmd{rootCmd: rootCmd,
		out: os.Stdout}
}

func (c *cmd) Spec() cli.CommandSpec {
	desc := `Show the details of the associated object of the hash.

If verbose option (-v) is provided show the content of the object.
If recursive option (-r) is provided walk any directory/deeptrees.

Hash string format: `
	return cli.CommandSpec{
		Name:    "inspect",
		Usage:   "[-r] [-v] HASH",
		Aliases: []string{"i"},
		Desc:    desc + c.rootCmd.HasherDescription(),
	}
}

func (c *cmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.verbose, "verbose", "v", false, "Show content of object")
	fl.BoolVarP(&c.recursive, "recursive", "r", false, "If hash is a tree, walk the tree")
}

func (c *cmd) Run(fl *pflag.FlagSet) {
	h, store := c.rootCmd.HashAndStoreFromSingleArg(fl)
	info, err := store.Query(h)
	if err != nil {
		log.Fatal(err)
	}
	if c.verbose {
		var err error
		switch info.Type {
		case cas.StoreItemTypeData:
			err = c.printData(h, store)
		case cas.StoreItemTypeDirList:
			err = c.printDirList(h, store)
		case cas.StoreItemTypeDirEntries:
			err = c.printDirEntries(h, store)
		case cas.StoreItemTypeDeepTree:
			err = c.printDeepTree(h, store)
		}
		if err != nil {
			log.Fatal(err)
		}
	} else {
		fmt.Printf("%s of size %d\n", info.Type, info.Size)
	}
}

func (c *cmd) printData(h hash.Hash, store cas.Store) error {
	f, err := store.OpenFile(h)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(c.out, f)
	return err
}

func (c *cmd) printDirList(h hash.Hash, store cas.Store) error {
	dl, err := store.GetDirList(h)
	if err != nil {
		return err
	}
	defer dl.Close()
	for {
		de, err := dl.Read()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		fmt.Fprintln(c.out, de.Name)
	}
}
func (c *cmd) printDirEntries(h hash.Hash, store cas.Store) error {
	printer := func(dh hash.Hash, de cas.DirEntry) error {
		if !c.recursive && !h.Equals(dh) {
			return fs.SkipDir
		}
		fmt.Fprintf(c.out, "%s %10o %16d %s\n", de.Hash, de.Mode, de.Size, de.Name)
		return nil
	}
	return cas.TreeWalk(h, store, printer)
}
func (c *cmd) printDeepTree(h hash.Hash, store cas.Store) error {
	printer := func(dh hash.Hash, de cas.DirEntry) error {
		fmt.Fprintf(c.out, "%s %10o %16d %s\n", de.Hash, de.Mode, de.Size, de.Name)
		return nil
	}
	return cas.DeepTreeWalk(h, store, printer)
}
