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
	"fmt"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/remote/servers"
	"github.com/google/abfs/shared/remote/servers/filebased"
)

type fileBasedCmd struct {
	*serverCmd
	levels   int
	localDir string
}

func (c *fileBasedCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "file-based",
		Usage:   "[-d <storage-dir>]",
		Aliases: []string{},
		Desc:    `Run a file-based ABFS server.`,
	}
}

func (c *fileBasedCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.IntVarP(&c.levels, "levels", "l", 3, "Number of nested directory levels (only 2 or 3)")
	fl.StringVarP(&c.localDir, "local-dir", "d", "/b/server-cache", "The local directory where files are stored")
}

func (c *fileBasedCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	return c.run(ctx)
}

func (c *fileBasedCmd) run(ctx context.Context) error {
	server, err := filebased.New(c.localDir,
		servers.NoLinkValidationOpt(c.noLinkValidation),
		servers.NoHashValidationOpt(c.noHashValidation),
		filebased.LevelsOpt(c.levels),
	)
	if err != nil {
		return fmt.Errorf("failed to create the ABFS server using directory %s: %w", c.localDir, err)
	}
	return c.Serve(ctx, server)
}
