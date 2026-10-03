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

package instance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
)

type renameCmd struct {
	*instanceCmd
}

func NewRenameCmd(instanceCmd *instanceCmd) *renameCmd {
	return &renameCmd{instanceCmd: instanceCmd}
}

func (c *renameCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "rename",
		Usage:   "<old-name> <new-name>",
		Aliases: []string{"mv"},
		Desc:    "Rename an ABFS instance and handles its associated services.",
	}
}

func (c *renameCmd) RegisterFlags(fl *pflag.FlagSet) {}

func (c *renameCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	oldName, newName := args.EnsureTwoArgs(fl)

	return c.run(oldName, newName)
}

func (c *renameCmd) run(oldName, newName string) error {
	if filepath.Base(newName) != newName {
		return fmt.Errorf("invalid new instance name %q: should not contain path separators", newName)
	}

	if oldName == newName {
		fmt.Printf("Instance names are identical. Nothing to do.\n")
		return nil
	}

	abfsBaseDir := c.Instance.BaseDir
	oldPath := filepath.Join(abfsBaseDir, oldName)
	newPath := filepath.Join(abfsBaseDir, newName)

	if _, err := os.Stat(oldPath); entities.IsNotExist(err) {
		return fmt.Errorf("instance %q does not exist", oldName)
	}

	if _, err := os.Stat(newPath); !entities.IsNotExist(err) {
		return fmt.Errorf("an instance named %q already exists", newName)
	}

	if err := EnsureInstanceNotInUse(oldName, c.Instance.BaseDir, c.Instance.Flavor.InstancesDirOpt); err != nil {
		return err
	}

	if err := removeCachemanService(oldName, c.Instance.BaseDir, c.Instance.Flavor.InstancesDirOpt); err != nil {
		return fmt.Errorf("failed to remove cacheman service: %w", err)
	}

	fmt.Printf("Renaming instance directory from %q to %q...\n", oldPath, newPath)
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("failed to rename instance directory: %w", err)
	}

	abfsBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find abfs executable path: %w", err)
	}
	cmd := execCommand(abfsBin, "--"+c.Instance.Flavor.InstancesDirOpt, abfsBaseDir, "--instance", newName, "cacheman", "setup")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	fmt.Printf("\nSuccessfully renamed instance %q to %q.\n", oldName, newName)
	return nil
}
