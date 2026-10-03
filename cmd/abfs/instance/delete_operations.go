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
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	cachemancmd "github.com/google/abfs/shared/cacheman/cmd"
	"github.com/google/abfs/shared/cmd"
)

// Package-level variables to allow mocking in unit tests
var (
	execCommand             = exec.Command
	checkSystemctlSupported = cachemancmd.SystemctlSupported
)

func EnsureInstanceNotInUse(targetInstance, abfsBaseDir, instanceDirOpt string) error {
	instancePath := filepath.Join(abfsBaseDir, targetInstance)
	cacheLockPath := filepath.Join(instancePath, "cache", "cache.lock")
	locked, err := cmd.IsCacheLocked(cacheLockPath)
	if err != nil {
		return err
	}
	if locked {
		log.Printf(`=====================================================================
WARNING: An ABFS process (cacheman or mount) is still running for instance %q.
The operation cannot proceed until this process is stopped.

SUGGESTED ACTION:
  1. Find the locking process by running this command in your terminal: fuser %s
  2. Search the PID to know the detail about the process: ps -P <PID>
  3. If the process is 'cacheman', stop it by running: abfs --%s %s --instance %s cacheman stop
  4. If the process is 'mount', please unmount it with abfs or kill the process
=====================================================================`, targetInstance, cacheLockPath, instanceDirOpt, abfsBaseDir, targetInstance)
		return errors.New("operation aborted due to active processes")
	}
	return nil
}

func removeCachemanService(targetInstance, abfsBaseDir, instanceDirOpt string) error {
	if !checkSystemctlSupported() {
		fmt.Printf("Systemd is not available, skipping removal.\n")
		return nil
	}

	// the inst.name may be different from the targetInstance
	cmd := execCommand("systemctl", "--user", "list-unit-files", "cacheman@"+targetInstance+".service", "-q")
	if err := cmd.Run(); err != nil {
		fmt.Printf("Service not found, skipping removal.\n")
		return nil
	}

	fmt.Printf("Removing cacheman service for instance %q...\n", targetInstance)
	abfsBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find abfs executable path: %w", err)
	}

	cmd = execCommand(abfsBin, "--"+instanceDirOpt, abfsBaseDir, "--instance", targetInstance, "cacheman", "remove")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to remove cacheman service: %w", err)
	}
	return nil
}
