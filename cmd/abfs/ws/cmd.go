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
	"text/tabwriter"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/workspaces"
)

type wsCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	*cmd.CachemanProvider
	*cmd.MountClientProvider
	workspaces *workspaces.Workspaces
	long       bool
}

func NewCmd(rootCmd *root.RootCmd) *wsCmd {
	return &wsCmd{
		RootCmd: rootCmd,
	}
}

func (c *wsCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "ws",
		Usage:       "[path]",
		Aliases:     []string{"work", "workspace"},
		Desc:        `Print the current workspace`,
		Subcommands: c.subcommands(),
	}
}

func (c *wsCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.long, "long", "l", false, "Long listing")
}

func (c *wsCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		newAttachCmd(c),
		newDetachCmd(c),
		newListCmd(c),
		newRemoveCmd(c),
		newCloneCmd(c),
		newMountCmd(c),
		newMountRepoCmd(c),
		newResolveCmd(c),
		newSnapshotCmd(c),
	}
}

func (c *wsCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	path := args.SingleArgOrDefault(fl, ".")
	return c.run(path)
}

func (c *wsCmd) initWorkspaces() {
	c.workspaces = workspaces.NewWorkspaces(c.Store, c.Cacheman)
}

func (c *wsCmd) run(path string) error {
	c.initWorkspaces()
	wsStatus, err := c.workspaces.StatusFromPath(path)
	if err != nil {
		if entities.IsNotExist(err) {
			return clierr.CLIErr("Cannot find any ABFS workspace in %q", path)
		}
		return err
	}

	if c.long {
		c.printLong(wsStatus)
	} else {
		c.printShort(wsStatus)
	}
	return nil
}

func (c *wsCmd) printShort(wsStatus binding.WorkspaceStatus) {
	status := ""
	if len(wsStatus.Conflicts) > 0 {
		status = " (conflicted)"
	}
	fmt.Printf("Workspace: %s%s\n", wsStatus.Name, status)
}

func (c *wsCmd) printLong(wsStatus binding.WorkspaceStatus) {
	status := ""
	conflicts := len(wsStatus.Conflicts)
	if conflicts > 0 {
		status = fmt.Sprintf(" (%d conflicts)", conflicts)
	}

	w := tabwriter.NewWriter(os.Stdout, 16, 0, 2, ' ', 0)
	defer func() { _ = w.Flush() }()

	_, _ = fmt.Fprintf(w, "Workspace:\t%s%s\n", wsStatus.Name, status)
	_, _ = fmt.Fprintf(w, "Path:\t%s\n", wsStatus.Path)
	if conflicts > 0 {
		_, _ = fmt.Fprintf(w, "Conflicts:\n")
		for _, s := range wsStatus.Conflicts {
			_, _ = fmt.Fprintf(w, "\t%s\n", s)
		}
	}
	_, _ = fmt.Fprintf(w, "Last Update:\t%s\n", wsStatus.LastUpdate)
	_, _ = fmt.Fprintf(w, "Snapshot Hash:\t%s\n", wsStatus.LastSnapshot)
}
