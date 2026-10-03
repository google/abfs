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

package deeptree

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type deepTreeCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	treeHash entities.Hash

	isCommit bool
	unpack   bool
}

func NewCmd(rootCmd *root.RootCmd) *deepTreeCmd {
	return &deepTreeCmd{RootCmd: rootCmd}
}

func (c *deepTreeCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "deeptree",
		Usage:   "[-c] <hash>",
		Hidden:  true,
		Aliases: []string{},
		Desc:    `Given a tree-hash it prints a deeptree hash associated with that tree`,
	}
}

func (c *deepTreeCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.isCommit, "commit", "c", false, "treat the input hash as commit instead of treehash")
	fl.BoolVarP(&c.unpack, "unpack", "u", false, "unpack the deep-tree at the given hash")
}

func (c *deepTreeCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	hexHash := args.EnsureSingleArg(fl)

	if err := c.dereferenceInputHash(hexHash); err != nil {
		return err
	}

	deeptree, err := binding.DeepTreeForTree(ctx, c.treeHash, c.Store)
	if err != nil {
		return err
	}
	fmt.Println(deeptree.Hash)

	if c.unpack {
		deepTreeCount, dirCount, err := binding.UnpackDeepTree(ctx, deeptree, c.Store)
		if err != nil {
			return err
		}
		fmt.Printf("DeepTree Count: %d\n", deepTreeCount)
		fmt.Printf("Directory Count: %d\n", dirCount)
	}
	return nil
}

func (c *deepTreeCmd) dereferenceInputHash(inputHash string) error {
	if _, err := hex.Decode(c.treeHash[:], []byte(inputHash)); err != nil {
		return err
	}

	if c.isCommit {
		git := casgit.New(c.Store)
		if commit, err := git.GetCommit(context.Background(), entities.NewHash(inputHash)); err != nil {
			return err
		} else {
			c.treeHash = entities.Hash(commit.TreeHash)
		}
	}

	return nil
}
