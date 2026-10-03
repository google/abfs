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

package cat

import (
	"bytes"
	"context"
	"io"
	"os"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
)

type catCmd struct {
	*root.RootCmd
	*cmd.StoreProvider

	object  bool
	project string
	server  string

	hashPath *hashpath.HashPath
	out      io.Writer
}

func NewCmd(rootCmd *root.RootCmd) *catCmd {
	return &catCmd{
		RootCmd: rootCmd,
		out:     os.Stdout,
	}
}

func (c *catCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "cat",
		Usage:   "<blob-hash-path>",
		Aliases: []string{},
		Desc:    `Show the content of a blob object`,
	}
}

func (c *catCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.object, "object", "", false, "Show the raw content of an object")
	fl.StringVarP(&c.project, "project", "p", entities.ABFSMetaProject, "The project that the hashpath belongs to")
	fl.StringVarP(&c.server, "server", "s", entities.ABFSMetaServer, "The server that the hashpath belongs to")
}

func (c *catCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	hp := args.EnsureSingleArg(fl)
	p, err := hashpath.Parse(hp)
	if err != nil {
		return clierr.CLIErr("Invalid hash-path: %v", err)
	}
	c.hashPath = p

	err = c.run(ctx)
	if entities.IsNotExist(err) {
		return clierr.CLIErr("Object %s cannot be found.", hp)
	}
	return err
}

func (c *catCmd) run(ctx context.Context) (retErr error) {
	res, err := hashpath.Resolve(ctx, c.Store, c.hashPath, c.server, c.project)
	if err != nil {
		return err
	}
	hash := res.Resolved

	hdr, err := c.Store.ObjectHeader(ctx, hash)
	if err != nil {
		return err
	}

	var reader io.ReadCloser
	if c.object && !hdr.IsBlob() {
		buf, err := c.Store.GetObject(ctx, hash)
		if err != nil {
			return err
		}
		reader = io.NopCloser(bytes.NewReader(buf))
	} else {
		if !hdr.IsBlob() {
			return clierr.CLIErr("Expected %s to be a blob but is %s.", hash, hdr.Type)
		}
		reader, err = c.Store.OpenBlob(ctx, hash)
		if err != nil {
			return err
		}
	}
	defer entities.CloseErrCheck(reader, &retErr)

	if _, err := io.Copy(c.out, reader); err != nil {
		return err
	}
	return nil
}
