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
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/iafan/cwalk"
	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
)

type removeCmd struct {
	*instanceCmd
	force bool
}

func NewRemoveCmd(instanceCmd *instanceCmd) *removeCmd {
	return &removeCmd{instanceCmd: instanceCmd}
}

func (c *removeCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "remove",
		Usage:   "<instance-name>",
		Aliases: []string{"rm"},
		Desc:    "Remove an ABFS instance and its associated data and services.",
	}
}

func (c *removeCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.force, "force", "f", false, "Force removal without confirmation prompt.")
}

func (c *removeCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	instanceToRemove := args.EnsureSingleArg(fl)

	return c.run(instanceToRemove)
}

func (c *removeCmd) run(instanceName string) error {
	abfsBaseDir := c.Instance.BaseDir
	instancePath := filepath.Join(abfsBaseDir, instanceName)

	if _, err := os.Stat(instancePath); entities.IsNotExist(err) {
		return fmt.Errorf("instance %q does not exist", instanceName)
	}

	// Prompt the user for confirmation.
	if !c.force {
		fmt.Printf("WARNING: This will permanently delete the instance %q and its associated data.\n", instanceName)
		scanner := bufio.NewScanner(os.Stdin)
		for {
			fmt.Print("Are you sure you want to continue? [yes/no]: ")
			scanner.Scan()
			answer := strings.ToLower(strings.TrimSpace(scanner.Text()))

			if answer == "yes" || answer == "y" {
				break
			}
			if answer == "no" || answer == "n" {
				fmt.Println("removal aborted.")
				return nil
			}
			fmt.Println("Invalid input. Please enter 'yes'(y) or 'no'(n).")
		}
	}

	fmt.Printf("Checking for active mounts/cacheman services associated with instance %q...\n", instanceName)
	if err := EnsureInstanceNotInUse(instanceName, c.Instance.BaseDir, c.Instance.Flavor.InstancesDirOpt); err != nil {
		return err
	}

	if err := removeCachemanService(instanceName, c.Instance.BaseDir, c.Instance.Flavor.InstancesDirOpt); err != nil {
		return fmt.Errorf("failed to remove cacheman service: %w", err)
	}

	tmpDir, err := os.MkdirTemp(abfsBaseDir, "instance-to-delete-")
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}

	tmpInstancePath := filepath.Join(tmpDir, instanceName)
	fmt.Printf("moving instance directory %q to %q\n", instancePath, tmpInstancePath)
	if err := os.Rename(instancePath, tmpInstancePath); err != nil {
		return fmt.Errorf("failed to move instance directory: %w", err)
	}

	fmt.Printf("Deleting directory: %q\n", tmpDir)
	if err := removeInstanceDir(tmpDir); err != nil {
		return fmt.Errorf("failed to delete directory %q: %w", tmpDir, err)
	}

	fmt.Printf("Successfully removed instance %q.\n", instanceName)
	return nil
}

func removeInstanceDir(instancePath string) error {
	toRemove := make(chan string)
	var wg sync.WaitGroup
	wg.Add(runtime.NumCPU())
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			defer wg.Done()
			for p := range toRemove {
				if err := os.Remove(p); err != nil && !entities.IsNotExist(err) {
					log.Printf("failed to remove %q: %v", p, err)
				}
			}
		}()
	}

	// Two-phases deletion
	// Phase 1: remove files concurrenctly
	log.Printf("Walking files in %q for removal...", instancePath)
	walkErr := cwalk.Walk(instancePath, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsDir() {
			toRemove <- filepath.Join(instancePath, path)
		}
		return nil
	})
	if walkErr != nil {
		log.Printf("error walking instance path %q for removal: %v", instancePath, walkErr)
	}
	close(toRemove)
	wg.Wait()

	// Phase 2: remove empty directories sequentially
	if err := os.RemoveAll(instancePath); err != nil {
		return fmt.Errorf("failed to remove empty directories %q: %w", instancePath, err)
	}

	return nil
}
