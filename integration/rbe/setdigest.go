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
	"io"
	"os"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/rbe/model"
)

type setDigestCmd struct {
	*App
	Out    io.Writer
	Sha256 string
	Size   int64
	Mode   uint32
}

func (c *setDigestCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "set-digest",
		Usage:   "--sha256=<hash> --size=<file size> --mode=<file mode> path",
		Aliases: []string{},
		Desc:    `Run set-digest`,
	}
}

func newSetDigestCmd(app *App) *setDigestCmd {
	return &setDigestCmd{
		App: app,
		Out: os.Stdout,
	}
}
func (c *setDigestCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVar(&c.Sha256, "sha256", "", "SHA-256 value")
	fl.Int64Var(&c.Size, "size", 0, "File size")
	fl.Uint32Var(&c.Mode, "mode", 0644, "File mode")
}
func (c *setDigestCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {

	path := args.EnsureSingleArg(fl)

	mode := c.Mode
	if mode&syscall.S_IFMT == 0 {
		mode |= syscall.S_IFREG
	}
	if err := c.Client().SetDigest(path, model.Digest{SHA256: c.Sha256, Size: c.Size}, mode); err != nil {
		return err
	}
	return nil
}
