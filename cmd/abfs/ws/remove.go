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
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
)

type removeCmd struct {
	*wsCmd
	force bool
	all   bool
}

func newRemoveCmd(c *wsCmd) *removeCmd {
	return &removeCmd{wsCmd: c}
}

func (c *removeCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "remove",
		Usage:   "<workspace-name>",
		Aliases: []string{"rm"},
		Desc:    `Remove a workspace`,
	}
}

func (c *removeCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.force, "force", "f", false, "Force remove workspace")
	fl.BoolVarP(&c.all, "all", "a", false, "Remove all workspaces")
}

func (c *removeCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.initWorkspaces()

	if c.all && fl.NArg() != 0 {
		return fmt.Errorf("cannot specify individual workspace names or patterns when using --all")
	}

	wsMap, err := c.workspaces.List()
	if err != nil {
		return fmt.Errorf("failed to list workspaces: %w", err)
	}

	var workspaces []string

	if c.all {
		for wsName := range wsMap {
			workspaces = append(workspaces, wsName)
		}
	} else {
		if fl.NArg() == 0 {
			return fmt.Errorf("must specify at least one workspace name, pattern, or use --all")
		}

		for _, arg := range fl.Args() {
			matchedAny := false
			for wsName := range wsMap {
				match, err := filepath.Match(arg, wsName)
				if err != nil {
					return fmt.Errorf("invalid pattern %q: %w", arg, err)
				}
				if match {
					workspaces = append(workspaces, wsName)
					matchedAny = true
				}
			}
			if !matchedAny && !containsWildcard(arg) {
				return fmt.Errorf("workspace %q not found", arg)
			}
		}
	}

	for _, ws := range workspaces {
		if err := c.exec(ctx, ws); err != nil {
			return err
		}
	}
	return nil
}

// Helper to determine if the user intended a wildcard pattern
func containsWildcard(s string) bool {
	return strings.ContainsAny(s, "*?[]")
}

func (c *removeCmd) exec(ctx context.Context, wsName string) error {
	hash, err := c.workspaces.SnapshotHash(wsName)
	if err != nil {
		return err
	}

	if !c.force {
		if !root.AskYesNo(ctx, fmt.Sprintf("Remove workspace %q with snapshot %s? [y/N]: ", wsName, hash)) {
			fmt.Printf("Workspace '%s' retained.\n", wsName)
			return nil
		}
	}

	if err := c.workspaces.Remove(wsName, hash); err != nil {
		return err
	}

	fmt.Printf("Workspace '%s' removed.\n", wsName)
	return nil
}
