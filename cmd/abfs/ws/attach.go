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
	"log"
	"os"
	"os/exec"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

type attachCmd struct {
	*wsCmd
	workDir     string
	wsName      string
	strategyStr string
	newWS       bool
	fromMount   bool
}

func newAttachCmd(c *wsCmd) *attachCmd {
	return &attachCmd{wsCmd: c}
}

func (c *attachCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "attach",
		Usage:   "<ws-name> [<work-dir>] [-s <strategy>] [-n]",
		Aliases: []string{},
		Desc:    `Attach an existing workspace to the specified work-dir.`,
		Hidden:  true,
	}
}

func (c *attachCmd) RegisterFlags(fl *pflag.FlagSet) {
	args.OptChoice(fl, &c.strategyStr, "strategy", "s", string(binding.PutStrategyExactMatch), []string{"exact-match", "local-wins", "remote-wins"}, "Strategy to use for the workspace (exact-match, local-wins, remote-wins)")
	fl.BoolVarP(&c.newWS, "new", "n", false, "Create a new workspace")
	fl.BoolVar(&c.fromMount, "from-mount", false, "Set by the mount that runs attach and unmounts the workspace when attach fails")
	if err := fl.MarkHidden("from-mount"); err != nil {
		panic(err)
	}
}

func (c *attachCmd) validateArgs(fl *pflag.FlagSet) error {
	switch len(fl.Args()) {
	case 0:
		return clierr.CLIUsage(fl, "No arguments specified.")
	case 1:
		c.wsName = fl.Arg(0)
		c.workDir = "."
	case 2:
		c.wsName = fl.Arg(0)
		c.workDir = fl.Arg(1)
	default:
		return clierr.CLIUsage(fl, "Too many arguments.")
	}
	if c.strategyStr != "" {
		if !binding.PutStrategy(c.strategyStr).IsValid() {
			return clierr.CLIErr("Invalid strategy: %s", c.strategyStr)
		}
	}
	return nil
}

func (c *attachCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.initWorkspaces()

	if err := c.validateArgs(fl); err != nil {
		return err
	}

	abfs, err := binding.NewClient(c.workDir, entities.ABFSSocketFileName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return err
	}
	defer func() {
		if err := abfs.Close(); err != nil {
			log.Printf("Error closing abfs client: %v", err)
		}
	}()

	strategy := binding.PutStrategy(c.strategyStr)
	if err := abfs.PutWS(c.wsName, c.newWS, strategy); err != nil {
		return err
	}

	// The snapshot does not keep the files setup writes for this client, so a restored
	// workspace needs them again.
	if c.Config.SyncAutoSetup && restoresSnapshot(c.newWS, strategy) {
		return autoSetup(ctx, c.workDir, !c.fromMount, runSetup)
	}
	return nil
}

// autoSetup runs setup in dir and fails like a mount or repo sync whose auto-setup fails. When
// the workspace stays attached, attaching again is refused, so the error says what to run.
func autoSetup(ctx context.Context, dir string, staysAttached bool, setup func(context.Context, string) error) error {
	err := setup(ctx, dir)
	switch {
	case err == nil:
		return nil
	case staysAttached:
		return fmt.Errorf("auto-setup failed (the workspace is attached, run abfs setup %s): %w", dir, err)
	default:
		return fmt.Errorf("auto-setup failed: %w", err)
	}
}

// restoresSnapshot reports whether attaching replaces the tree with the workspace snapshot.
func restoresSnapshot(isNew bool, strategy binding.PutStrategy) bool {
	return !isNew && strategy == binding.PutStrategyRemoteWin
}

func runSetup(ctx context.Context, dir string) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "setup", dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
