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

package testclient

import (
	"context"
	"log"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

type executeCmd struct {
	*aresTestClientCmd
	patchHashesStr []string
	storeMapResult bool
	prefetchInputs bool
}

func (c *executeCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "execute",
		Usage:   "[-p <patch hash>,<patch hash>] [-s=false] <hash>",
		Aliases: []string{},
		Desc:    `run ares Execute with given eval request hash`,
	}
}

func (c *executeCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringSliceVarP(&c.patchHashesStr, "patches", "p", nil, "Patches that should be applied before evaluating the request")
	fl.BoolVarP(&c.storeMapResult, "store-map-result", "s", false, "Store the mapping from request hash to response hash")
	fl.BoolVarP(&c.prefetchInputs, "prefetch-input", "r", false, "Prefetch the inputs concorrently with execution")
}

func (c *executeCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	hashStr := args.EnsureSingleArg(fl)
	if !entities.IsHash(hashStr) {
		return clierr.CLIErr("Invalid hash %q", hashStr)
	}
	hash := entities.NewHash(hashStr)

	patchHashes := make([]entities.Hash, 0, len(c.patchHashesStr))
	for _, s := range c.patchHashesStr {
		if !entities.IsHash(s) {
			return clierr.CLIErr("One of the provided patch hashes %q was not valid", s)
		}
		h := entities.NewHash(s)
		patchHashes = append(patchHashes, h)
	}

	return c.run(ctx, hash, patchHashes)
}

func (c *executeCmd) run(ctx context.Context, hash entities.Hash, patchHashes []entities.Hash) error {
	cli := c.Client()
	defer func() {
		if err := cli.Close(); err != nil {
			log.Printf("WARNING: error closing client: %v", err)
		}
	}()
	evalResp, err := cli.Execute(ctx, hash, patchHashes, c.storeMapResult, c.prefetchInputs)
	if err != nil {
		return err
	}
	log.Printf("Success. New Eval Response Hash: %q", evalResp)
	return nil
}
