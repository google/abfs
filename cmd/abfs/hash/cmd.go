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

package hash

import (
	"context"
	"fmt"
	"log"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
)

type hashCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	path       string
	setHashHex string
}

func NewCmd(rootCmd *root.RootCmd) *hashCmd {
	return &hashCmd{RootCmd: rootCmd}
}

func (c *hashCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "hash",
		Usage:   "[-c] [--set <hash>] <hash-path>|<path>",
		Aliases: []string{"h"},
		Desc:    `Print the hash of a given file.`,
	}
}

func (c *hashCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVar(&c.setHashHex, "set", "", "Set the hash of a given path")
}

func (c *hashCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	arg := args.EnsureSingleArg(fl)
	hp, err := hashpath.Parse(args.EnsureSingleArg(fl))
	if err != nil {
		c.path = arg
	} else {
		return c.handleHashPath(ctx, hp)
	}

	err = c.printFileHash()
	if err == nil { // No errors means we are good to go
		return nil
	}
	if entities.IsNotExist(err) {
		return clierr.CLIErr("%q is not a hash-path or an existing file or dir", arg)
	}
	return err
}

func (c *hashCmd) handleHashPath(ctx context.Context, hp *hashpath.HashPath) error {
	res, err := hashpath.Resolve(ctx, c.Store, hp, entities.ABFSMetaServer, entities.ABFSMetaProject)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Resolved)
	return nil
}

func (c *hashCmd) printFileHash() error {
	if c.setHashHex == "" {
		hash, err := bindcas.Hash(c.path)
		if err != nil {
			return err
		}
		fmt.Println(hash.String())
	} else {
		hash := entities.NewHash(c.setHashHex)
		err := bindcas.SetHash(c.path, hash)
		if err != nil {
			return err
		}
	}
	return nil
}
