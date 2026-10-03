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
	"fmt"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
)

type addMapResultCmd struct {
	*testClientCmd
}

func NewAddMapResultCmd(tc *testClientCmd) *addMapResultCmd {
	return &addMapResultCmd{testClientCmd: tc}
}

func (c *addMapResultCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "add-map-result",
		Usage:   "<from-hash> <to-hash>",
		Aliases: []string{},
		Desc:    `Add a map result entry from the given hash to the other given one.`,
	}
}

func (c *addMapResultCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *addMapResultCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	fromHex, toHex := args.EnsureTwoArgs(fl)
	fromHash := entities.NewHash(fromHex)
	toHash := entities.NewHash(toHex)

	err := c.ABFSClient.AddMapResult(ctx, fromHash, toHash)
	if err != nil {
		return fmt.Errorf("failed to exec command: %w", err)
	}
	fmt.Printf("done.\n")
	return nil
}
