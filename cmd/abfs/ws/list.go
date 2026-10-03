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

package ws

import (
	"context"
	"fmt"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/mergestat/timediff"
	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
)

type listCmd struct {
	*wsCmd
	long bool
}

func newListCmd(c *wsCmd) *listCmd {
	return &listCmd{wsCmd: c}
}

func (c *listCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "list",
		Usage:   "",
		Aliases: []string{"ls"},
		Desc:    `List the workspaces for this user`,
	}
}

func (c *listCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.long, "long", "l", false, "List workspaces in long format")
}

func (c *listCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.initWorkspaces()

	args.EnsureNoArgs(fl)

	return c.exec(ctx)
}

func (c *listCmd) exec(ctx context.Context) error {
	wsMap, err := c.workspaces.List()
	if err != nil {
		return err
	}

	names := make([]string, 0, len(wsMap))
	hashes := make([]entities.Hash, 0, len(wsMap))
	sorted := make([]int, 0, len(wsMap))
	for wsName, hash := range wsMap {
		names = append(names, wsName)
		hashes = append(hashes, hash)
		sorted = append(sorted, len(sorted))
	}

	snaps, err := binding.FetchMultiObject[binding.WSSnapshot](ctx, c.workspaces.Store, hashes)
	if err != nil {
		return err
	}

	slices.SortFunc(sorted, func(a, b int) int {
		return -snaps[a].Time.Compare(snaps[b].Time)
	})

	var w *tabwriter.Writer
	if c.long {
		w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		defer func() { _ = w.Flush() }()
	}

	for _, i := range sorted {
		if c.long {
			_, _ = fmt.Fprintf(w, "%s\t%s\tLast changed %s\n", names[i], hashes[i], timediff.TimeDiff(snaps[i].Time))
		} else {
			_, _ = fmt.Printf("%s\n", names[i])
		}
	}
	return nil
}
