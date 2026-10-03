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

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
)

type debugCmd struct {
	*cachemanCmd
	*cmd.CachemanProvider
	debug *bool
}

func NewDebugCmd(c *cachemanCmd) *debugCmd {
	return &debugCmd{cachemanCmd: c}
}

func (c *debugCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "debug",
		Usage: "[<true|false>]",
		Desc:  `Enables or disables cacheman debug logging.`,
	}
}

func (c *debugCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *debugCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if fl.NArg() != 0 {
		mode := args.EnsureSingleArg(fl)
		if mode != "true" && mode != "false" {
			return clierr.CLIErr("Invalid mode. Must be 'true' or 'false'.")
		}
		enable := mode == "true"
		c.debug = &enable
	}
	if c.debug == nil {
		enabled, err := c.Cacheman.GetDebug()
		if err != nil {
			return err
		}
		fmt.Println(enabled)
	} else {
		if err := c.Cacheman.SetDebug(*c.debug); err != nil {
			return err
		}
	}
	return nil
}
