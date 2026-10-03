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

package put

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

type putCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	filepath string
	out      io.Writer
}

func NewCmd(rootCmd *root.RootCmd) *putCmd {
	return &putCmd{RootCmd: rootCmd, out: os.Stdout}
}

func (c *putCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "put",
		Usage:   "[filepath]",
		Aliases: []string{},
		Desc:    `Put a file into cached storage and returns a hash to the object. If filepath is omitted, read in from stdin. For large objects, use a file.`,
	}
}

func (c *putCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *putCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.filepath = args.EnsureSingleArg(fl)

	return c.run(ctx)
}
func (c *putCmd) run(ctx context.Context) (retErr error) {
	var err error
	var reader io.Reader
	var hash entities.Hash
	var size int64
	if c.filepath != "" {
		hash, size, err = objects.HashFileBlob(c.filepath)
		if err != nil {
			return fmt.Errorf("%v", err)
		}
		file, err := os.Open(c.filepath)
		if err != nil {
			return fmt.Errorf("failed to open file %s: %w", c.filepath, err)
		}
		defer func() { retErr = entities.FirstError(retErr, file.Close()) }()
		reader = file
	} else {
		buf := new(bytes.Buffer)
		if _, err := io.Copy(buf, os.Stdin); err != nil {
			return fmt.Errorf("failed to read from stdin: %w", err)
		}
		hash = objects.HashBlobBytes(buf.Bytes())
		size = int64(buf.Len())
		reader = buf
	}

	if err := c.Store.WriteBlob(ctx, hash, size, reader); err != nil {
		return fmt.Errorf("failed to write blob: %w", err)
	}

	_, err = fmt.Fprintf(c.out, "%s\n", hash)
	return err
}
