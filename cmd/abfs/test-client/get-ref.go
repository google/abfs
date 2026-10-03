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
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

type getRefCmd struct {
	*testClientCmd
	*cmd.ConfigProvider
	project string
	server  string
}

func NewGetRefCmd(tc *testClientCmd) *getRefCmd {
	return &getRefCmd{testClientCmd: tc}
}

func (c *getRefCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "get-ref",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Read refs from stdin and gets their associated hashes from remote server`,
	}
}

func (c *getRefCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.project, "project", "p", "", "The project that the refs belong to")
	fl.StringVarP(&c.server, "server", "s", "", "The server that the refs belong to (default is the manifest-server)")
}

func (c *getRefCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	if c.project == "" {
		return clierr.CLIErr("project is not specified")
	}

	if c.server == "" {
		c.server = c.Config.ManifestServer
	}

	refs, err := readRefs(os.Stdin)
	if err != nil {
		return fmt.Errorf("error while reading the input: %w", err)
	}

	hashes, err := c.ABFSClient.GetRefs(ctx, c.server, c.project, refs)
	if err != nil {
		return fmt.Errorf("failed to exec command: %w", err)
	}
	for i, r := range refs {
		h := hashes[i]
		if h == entities.ZeroHash {
			fmt.Printf("%40s %s\n", "(not exists)", r)
		} else {
			fmt.Printf("%s %s\n", h, r)
		}
	}
	return nil
}

func readRefs(reader io.Reader) ([]string, error) {
	result := make([]string, 0)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		result = append(result, strings.TrimSpace(scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
