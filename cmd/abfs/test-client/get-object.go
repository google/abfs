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
)

type getObjectCmd struct {
	*testClientCmd
	blob bool
}

func NewGetObjectCmd(tc *testClientCmd) *getObjectCmd {
	return &getObjectCmd{testClientCmd: tc}
}

func (c *getObjectCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "get-object",
		Usage:   "<hash> [--blob=false]",
		Aliases: []string{},
		Desc:    `Gets a object with the given hash and prints it to stdout.`,
	}
}

func (c *getObjectCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.blob, "blob", "b", true, "Treat the input as a blob (default=true)")
}

func (c *getObjectCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	hashHex := args.EnsureSingleArg(fl)
	hash := entities.NewHash(hashHex)

	return c.run(ctx, hash)
}

func (c *getObjectCmd) run(ctx context.Context, hash entities.Hash) (retErr error) {
	reader, err := c.ABFSClient.GetObject(ctx, hash)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	if c.blob {
		t, _, err := objects.ReadHeader(reader)
		if err != nil {
			return err
		}
		if t != string(entities.ObjectTypeBlob) {
			return fmt.Errorf("expected blob type, got %s", t)
		}
	}
	_, err = io.Copy(os.Stdout, reader)
	return err
}
