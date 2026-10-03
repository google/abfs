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
	"path/filepath"
	"syscall"

	"github.com/gofrs/flock"

	"github.com/google/abfs/shared/entities"
)

// MountPoint finds the mount point for a given path by traversing upwards and comparing device IDs.
func MountPoint(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path for %q: %w", path, err)
	}

	initialStat, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat initial path %q: %w", absPath, err)
	}

	initialSysStat, ok := initialStat.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("could not get syscall.Stat_t from initial path %q", absPath)
	}
	initialDev := initialSysStat.Dev

	currentPath := absPath

	for {
		if currentPath == "/" {
			return "/", nil
		}

		parentPath := filepath.Dir(currentPath)

		// Stat the parent directory
		parentStat, err := os.Stat(parentPath)
		if err != nil {
			return "", fmt.Errorf("failed to stat parent path %q: %w", parentPath, err)
		}

		parentSysStat, ok := parentStat.Sys().(*syscall.Stat_t)
		if !ok {
			return "", fmt.Errorf("could not get syscall.Stat_t from parent path %q", parentPath)
		}
		parentDev := parentSysStat.Dev

		// If the device ID of the parent is different from the initial path's device ID,
		// then the currentPath (before moving up) is the mount point.
		if initialDev != parentDev {
			return currentPath, nil
		}

		// If parentPath is the same as currentPath (e.g., already at root), break to prevent infinite loop
		if parentPath == currentPath {
			// This case usually means the original path is on the root filesystem,
			// and we've traversed all the way up.
			return "/", nil
		}

		currentPath = parentPath
	}
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
