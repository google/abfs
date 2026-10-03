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
	"io"
	"os"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
)

type putObjectCmd struct {
	*testClientCmd
	hashHex string
	hash    entities.Hash
	blob    bool
}

func NewPutObjectCmd(tc *testClientCmd) *putObjectCmd {
	return &putObjectCmd{testClientCmd: tc}
}

func (c *putObjectCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "put-object",
		Usage:   "[-h <hash>] [--blob=false]",
		Aliases: []string{},
		Desc:    `Read a object from stdin and put it to the remote server`,
	}
}

func (c *putObjectCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.hashHex, "hash", "h", "", "The hash to validate the input")
	fl.BoolVarP(&c.blob, "blob", "b", true, "Treat the input as a blob (default=true)")
}

func (c *putObjectCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	c.hash = entities.NewHash(c.hashHex)

	return c.run(ctx)
}
func (c *putObjectCmd) run(ctx context.Context) error {
	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}

	var contentHash entities.Hash
	if c.blob {
		contentHash = objects.HashBlobBytes(content)
	} else {
		contentHash = objects.HashBytes(content)
	}

	if !c.hash.IsZero() && c.hash != contentHash {
		return fmt.Errorf("the specified hash %s does not match the content hash %s", c.hash, contentHash)
	}

	var reader remote.ContentReadCloser
	if c.blob {
		reader = remote.NewBlobReaderFromBytes(content)
	} else {
		reader = remote.ContentReaderFromBytes(content)
	}
	if err := c.ABFSClient.PutObject(ctx, contentHash, reader); err != nil {
		return err
	}
	fmt.Printf("Recorded %d bytes as %s\n", len(content), contentHash)
	return nil
}
