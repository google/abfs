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

package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
)

type getDigestCmd struct {
	*App
	Out io.Writer
}

func newGetDigestCmd(app *App) *getDigestCmd {
	return &getDigestCmd{
		App: app,
		Out: os.Stdout,
	}
}

func (c *getDigestCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "get-digest",
		Usage:   "path",
		Aliases: []string{},
		Desc:    `Get the RBE digest for a given path`,
	}
}

func (c *getDigestCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *getDigestCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	path := args.EnsureSingleArg(fl)

	stat, err := c.Client().GetDigest(path)
	if err != nil {
		return err
	}

	fmt.Fprintf(c.Out, "Path:   %s\nSHA256: %s\nSize:   %d\nMode:   %#o\n",
		stat.Path, stat.Digest.SHA256, stat.Digest.Size, stat.Mode)

	return nil
}
