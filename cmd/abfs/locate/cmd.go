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

package locate

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
)

type locateCmd struct {
	*root.RootCmd
}

func NewCmd(rootCmd *root.RootCmd) *locateCmd {
	return &locateCmd{RootCmd: rootCmd}
}

func (c *locateCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "locate",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Locate 'abfs' binaries on PATH and display their version.`,
		Hidden:  true,
	}
}

func (c *locateCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *locateCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return c.run()
}

func (c *locateCmd) run() error {
	path := os.Getenv("PATH")
	paths := filepath.SplitList(path)
	seen := make(map[string]bool)
	curr, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not get current executable: %w", err)
	}

	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		tgt := filepath.Join(abs, "abfs")
		if _, err := os.Stat(tgt); err != nil {
			continue
		}
		b, err := exec.Command(tgt, "version").Output()
		if err != nil {
			return fmt.Errorf("could not exec abfs: %w", err)
		}
		version := string(bytes.TrimSpace(b))
		maybeStar := ""
		if tgt == curr {
			maybeStar = "*"
		}
		fmt.Printf("%s%s : %s\n", maybeStar, tgt, version)
	}

	return nil
}
