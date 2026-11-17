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

package cmd

import (
	"fmt"
	"os"

	"github.com/gofrs/flock"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

// MountPoint finds the mount point for a given path by traversing upwards and comparing device IDs.
func MountPoint(path string) (string, error) {
	return bindcas.MountPoint(path)
}

func IsCacheLocked(cacheLockPath string) (bool, error) {
	if _, err := os.Stat(cacheLockPath); entities.IsNotExist(err) {
		return false, nil
	}

	fileLock := flock.New(cacheLockPath)

	locked, err := fileLock.TryLock()
	if err != nil {
		return false, fmt.Errorf("could not test the cache lock at %s: %w", cacheLockPath, err)
	}

	if locked {
		return false, fileLock.Unlock()
	}

	return true, nil
}
