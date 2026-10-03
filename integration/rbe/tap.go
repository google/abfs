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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/google/abfs/cas/fs"
	"github.com/spf13/pflag"

	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/shared/cmd"
)

type tapCmd struct {
	*App
	pretty bool
	stdin  bool
	Out    io.Writer
}
type RBETapResult struct {
	Reads   []filesystem.RBEPathStat `json:"reads"`
	Writes  []filesystem.RBEPathStat `json:"writes"`
	Deletes map[string]struct{}      `json:"deletes"`
	fs.TapInfo
}

func newTapCmd(app *App) *tapCmd {
	return &tapCmd{
		App: app,
		Out: os.Stdout,
	}
}

func (c *tapCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "tap",
		Usage:   "[flags] -- <command> [args...]",
		Aliases: []string{},
		Desc:    `Run RBE tapping on input command`,
	}
}

func (c *tapCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.pretty, "pretty", false, "Pretty print JSON output")
	fl.BoolVar(&c.stdin, "stdin", false, "Pass stdin to the underlying command process")
}

func (c *tapCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {

	cmdArgs := fl.Args()
	if len(cmdArgs) == 0 {
		return fmt.Errorf("no command provided to tap")
	}

	subCmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	if c.stdin {
		subCmd.Stdin = os.Stdin
	}

	resolvedTapResult, err := c.Client().CASFS().TapResolved(subCmd)
	if err != nil {
		return fmt.Errorf("failed to run bindcas.TapResolved: %w", err)
	}

	digestResp, err := c.Client().TapResolve(resolvedTapResult)
	if err != nil {
		return fmt.Errorf("failed to resolve tap through mount: %w", err)
	}

	output := RBETapResult{
		Reads:   digestResp.Reads,
		Writes:  digestResp.Writes,
		Deletes: resolvedTapResult.Deletes,
		TapInfo: resolvedTapResult.TapInfo,
	}

	out := c.Out
	if out == nil {
		out = os.Stdout
	}

	enc := json.NewEncoder(out)
	if c.pretty {
		enc.SetIndent("", "  ")
	}

	if err := enc.Encode(output); err != nil {
		return fmt.Errorf("JSON encoding failure: %w", err)
	}

	return nil
}
