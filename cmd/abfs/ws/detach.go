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

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type detachCmd struct {
	*wsCmd
	force bool
}

func newDetachCmd(c *wsCmd) *detachCmd {
	return &detachCmd{wsCmd: c}
}

func (c *detachCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "detach",
		Usage:   "<work-dir> [-f]",
		Aliases: []string{},
		Desc:    `Detaches a workspace from the given workdir.`,
		Hidden:  true,
	}
}

func (c *detachCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.force, "force", "f", false, "Do not ask for confirmation.")
}

func (c *detachCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.initWorkspaces()

	workDir := args.SingleArgOrDefault(fl, ".")

	abfs, err := binding.NewClient(workDir, entities.ABFSSocketFileName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return err
	}
	defer func() {
		if err := abfs.Close(); err != nil {
			log.Printf("Error closing abfs client: %v", err)
		}
	}()

	ws, err := abfs.GetWS()
	if err != nil {
		return err
	}
	if !c.force {
		if !root.AskYesNo(ctx, fmt.Sprintf("Detach workspace %q from %q? [y/N]: ", ws.Name, abfs.Root())) {
			return clierr.CLIErr("User canceled workspace detachment.")
		}
	}
	if err := abfs.RemoveWS(); err != nil {
		return err
	}
	fmt.Printf("Detached from workspace '%s'.\n", ws.Name)
	return nil
}
