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

package cacheman

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cacheman/server"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
)

type healthBypassCmd struct {
	*cachemanCmd
}

func NewHealthBypassCmd(c *cachemanCmd) *healthBypassCmd {
	return &healthBypassCmd{cachemanCmd: c}
}

func (c *healthBypassCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:        "health-bypass",
		Usage:       "[set|clear|show]",
		Aliases:     []string{},
		Desc:        "Manage the health bypass file for cacheman.",
		Subcommands: c.subcommands(),
	}
}

func (c *healthBypassCmd) RegisterFlags(fl *pflag.FlagSet) {}

func (c *healthBypassCmd) subcommands() []cmd.ABFSCmd {
	return []cmd.ABFSCmd{
		NewHealthBypassSetCmd(c),
		NewHealthBypassClearCmd(c),
		NewHealthBypassShowCmd(c),
	}
}

func (c *healthBypassCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	return c.show()
}

func (c *healthBypassCmd) bypassFilePath() string {
	return filepath.Join(c.Instance.Config.CacheDir, server.StatusDirName, server.HealthBypassFileName)
}

func (c *healthBypassCmd) show() error {
	path := c.bypassFilePath()
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Health bypass is enabled (%s exists)\n", path)
	} else {
		fmt.Printf("Health bypass is disabled (%s does not exist)\n", path)
	}
	return nil
}

type healthBypassSetCmd struct {
	*healthBypassCmd
}

func NewHealthBypassSetCmd(parent *healthBypassCmd) *healthBypassSetCmd {
	return &healthBypassSetCmd{healthBypassCmd: parent}
}

func (c *healthBypassSetCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "set",
		Usage:   "",
		Aliases: []string{},
		Desc:    "Enable health bypass by creating the health_bypass file.",
	}
}

func (c *healthBypassSetCmd) RegisterFlags(fl *pflag.FlagSet) {}

func (c *healthBypassSetCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	path := c.bypassFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		return fmt.Errorf("failed to create health bypass file: %w", err)
	}
	fmt.Printf("Health bypass enabled (%s)\n", path)
	return nil
}

type healthBypassClearCmd struct {
	*healthBypassCmd
}

func NewHealthBypassClearCmd(parent *healthBypassCmd) *healthBypassClearCmd {
	return &healthBypassClearCmd{healthBypassCmd: parent}
}

func (c *healthBypassClearCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "clear",
		Usage:   "",
		Aliases: []string{},
		Desc:    "Disable health bypass by removing the health_bypass file.",
	}
}

func (c *healthBypassClearCmd) RegisterFlags(fl *pflag.FlagSet) {}

func (c *healthBypassClearCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	path := c.bypassFilePath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove health bypass file: %w", err)
	}
	fmt.Printf("Health bypass cleared (%s)\n", path)
	return nil
}

type healthBypassShowCmd struct {
	*healthBypassCmd
}

func NewHealthBypassShowCmd(parent *healthBypassCmd) *healthBypassShowCmd {
	return &healthBypassShowCmd{healthBypassCmd: parent}
}

func (c *healthBypassShowCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "show",
		Usage:   "",
		Aliases: []string{},
		Desc:    "Show whether health bypass is enabled or disabled.",
	}
}

func (c *healthBypassShowCmd) RegisterFlags(fl *pflag.FlagSet) {}

func (c *healthBypassShowCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	return c.show()
}
