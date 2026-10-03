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
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

type headersCmd struct {
	*testClientCmd
}

func NewHeadersCmd(tc *testClientCmd) *headersCmd {
	return &headersCmd{testClientCmd: tc}
}

func (c *headersCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "headers",
		Usage:   "<hash>...",
		Aliases: []string{},
		Desc:    `Get the headers of the specified objects.`,
	}
}

func (c *headersCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *headersCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args := args.EnsureArgs(fl)

	hashes := make([]entities.Hash, len(args))
	for i, arg := range args {
		hash := entities.NewHash(arg)
		if hash.IsZero() {
			return clierr.CLIErr("Invalid hash %q", arg)
		}
		hashes[i] = hash
	}

	return c.run(ctx, hashes)
}

func (c *headersCmd) run(ctx context.Context, hashes []entities.Hash) error {
	hdrs, err := c.ABFSClient.Headers(ctx, hashes)
	if err != nil {
		return err
	}
	for i, hdr := range hdrs {
		if !hdr.IsEmpty() {
			fmt.Printf("%s %s %d\n", hashes[i], hdr.Type, hdr.Size)
		}
	}
	return nil
}
