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
	"os/exec"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
)

type createCmd struct {
	*instanceCmd
}

func NewCreateCmd(instanceCmd *instanceCmd) *createCmd {
	return &createCmd{instanceCmd: instanceCmd}
}

func (c *createCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "create",
		Usage: "<new-instance-name>",
		Desc:  "Create a new ABFS instance.",
	}
}

func (c *createCmd) RegisterFlags(fl *pflag.FlagSet) {}

func (c *createCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	newInstanceName := args.EnsureSingleArg(fl)

	return c.run(newInstanceName)
}

func (c *createCmd) run(newInstanceName string) error {
	if filepath.Base(newInstanceName) != newInstanceName {
		return fmt.Errorf("invalid instance name %q: should not contain path separators", newInstanceName)
	}

	abfsBaseDir := c.Instance.BaseDir
	instancePath := filepath.Join(abfsBaseDir, newInstanceName)

	_, err := os.Stat(instancePath)
	if err == nil {
		return fmt.Errorf("instance %q already exists at %q", newInstanceName, instancePath)
	}
	if !entities.IsNotExist(err) {
		return fmt.Errorf("failed to check for instance at %q: %w", instancePath, err)
	}

	fmt.Printf("Creating new instance %q...\n\n", newInstanceName)

	abfsBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find abfs executable path: %w", err)
	}

	cmd := exec.Command(abfsBin, c.Instance.Flavor.DashedInstancesDir(), abfsBaseDir, "--instance", newInstanceName, "init")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to initialize new instance: %w", err)
	}

	fmt.Printf("\nSuccessfully created instance %q.\n", newInstanceName)
	fmt.Printf("You can now use it with the --instance flag, for example:\n")
	fmt.Printf("  abfs --instance %s cacheman status\n", newInstanceName)
	return nil
}
