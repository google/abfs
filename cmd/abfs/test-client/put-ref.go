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
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
)

type putRefCmd struct {
	*testClientCmd
	*cmd.ConfigProvider
	project string
	server  string
}

func NewPutRefCmd(tc *testClientCmd) *putRefCmd {
	return &putRefCmd{testClientCmd: tc}
}

func (c *putRefCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "put-ref",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Read hash and ref lines from stdin and send them to the remote server`,
	}
}

func (c *putRefCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.project, "project", "p", "", "The project that the refs belong to")
	fl.StringVarP(&c.server, "server", "s", "", "The server that the refs belong to (default is the manifest-server)")
}

func (c *putRefCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	if c.project == "" {
		return fmt.Errorf("project is not specified")
	}

	if c.server == "" {
		c.server = c.Config.ManifestServer
	}

	refs, err := readRefHashes(os.Stdin)
	if err != nil {
		return fmt.Errorf("error while reading the input: %w", err)
	}

	if err := c.ABFSClient.PutRefs(ctx, c.server, c.project, refs); err != nil {
		return fmt.Errorf("failed to exec command: %w", err)
	}
	fmt.Printf("Put %d refs.\n", len(refs))
	return nil
}

func readRefHashes(reader io.Reader) ([]entities.RefHash, error) {
	result := make([]entities.RefHash, 0)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !entities.IsHash(fields[0]) {
			fmt.Println("expected a pair of hash and reference: <hash> <ref>")
			continue
		}
		result = append(result, entities.RefHash{Hash: entities.NewHash(fields[0]), Ref: fields[1]})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
