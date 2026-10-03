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
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/entities"
)

type searchRefCmd struct {
	*testClientCmd
	*cmd.ConfigProvider
	project string
	server  string
	ref     string
	limit   int
	json    bool
}

func NewSearchRefCmd(tc *testClientCmd) *searchRefCmd {
	return &searchRefCmd{testClientCmd: tc}
}

func (c *searchRefCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "search-ref",
		Usage:   "[-p <project>] [-s <server>] [-r <ref>] [-n <limit>] [--json]",
		Aliases: []string{},
		Desc:    `Search for a ref across multiple projects by a given ref-prefix`,
	}
}

func (c *searchRefCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.project, "project", "p", "", "The project that the refs belong to")
	fl.StringVarP(&c.server, "server", "s", "", "The server that the refs belong to (default is the manifest-server)")
	fl.StringVarP(&c.ref, "ref", "r", "", "The reference prefix")
	fl.IntVarP(&c.limit, "limit", "n", 0, "The maximum number of references to return")
	fl.BoolVar(&c.json, "json", false, "output the outcome as json")
}

func (c *searchRefCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if c.server == "" {
		c.server = c.Config.ManifestServer
	}

	refs, err := c.ABFSClient.SearchRefs(ctx, c.server, c.project, c.ref, c.limit)
	if err != nil {
		return fmt.Errorf("failed to exec command: %w", err)
	}

	if c.json {
		if err := outputAsJson(refs); err != nil {
			return err
		}
	} else {
		for _, r := range refs {
			fmt.Printf("%s %s %s\n", r.Project, r.Hash, r.Ref)
		}
	}
	return nil
}

type SearchResp struct {
	Ref  string
	Hash string
}

func outputAsJson(refs []entities.ProjectRefHash) error {
	out := make([]SearchResp, 0, len(refs))

	for _, r := range refs {
		out = append(out, SearchResp{
			Ref:  r.Ref,
			Hash: r.Hash.String(),
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("  ", "    ")
	return enc.Encode(out)
}
