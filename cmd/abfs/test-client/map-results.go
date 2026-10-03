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

type mapResultsCmd struct {
	*testClientCmd
}

func NewMapResultsCmd(tc *testClientCmd) *mapResultsCmd {
	return &mapResultsCmd{testClientCmd: tc}
}

func (c *mapResultsCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "map-results",
		Usage:   "<hash>",
		Aliases: []string{},
		Desc:    `Get and print the map results for a given hash.`,
	}
}

func (c *mapResultsCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *mapResultsCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	hashHex := args.EnsureSingleArg(fl)
	hash := entities.NewHash(hashHex)

	result, err := c.ABFSClient.MapResults(ctx, hash)
	if err != nil {
		return fmt.Errorf("failed to exec command: %w", err)
	}

	for _, h := range result {
		fmt.Println(h)
	}
	return nil
}
