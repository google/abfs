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

package server

import (
	"context"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

type inMemoryCmd struct {
	*serverCmd
	compressed bool
}

func (c *inMemoryCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "in-memory",
		Usage:   "[-c]",
		Aliases: []string{},
		Desc:    `Run an in-memory ABFS server.`,
	}
}

func (c *inMemoryCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.compressed, "compressed", "c", false, "Enable ZSTD compression")
}

func (c *inMemoryCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return c.run(ctx)
}

func (c *inMemoryCmd) run(ctx context.Context) error {
	server := inmemory.New()
	return c.Serve(ctx, server)
}
