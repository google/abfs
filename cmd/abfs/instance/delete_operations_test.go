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
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureInstanceNotInUse(t *testing.T) {
	baseDir, inst := setupMockInstances(t)

	// 1. Lock the "default" instance
	defaultLockPath := filepath.Join(baseDir, "default", "cache", "cache.lock")
	defaultLock := flock.New(defaultLockPath)
	locked, err := defaultLock.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	defer func() {
		err := defaultLock.Unlock()
		require.NoError(t, err)
	}()

	// 2. Check if "target" is in use.
	err = EnsureInstanceNotInUse("target", inst.BaseDir, inst.Flavor.InstancesDirOpt)
	assert.NoError(t, err, "target instance should not be detected as in use")

	// 3. Lock the "target" instance
	targetLockPath := filepath.Join(baseDir, "target", "cache", "cache.lock")
	targetLock := flock.New(targetLockPath)
	tLocked, err := targetLock.TryLock()
	require.NoError(t, err)
	require.True(t, tLocked)
	defer func() {
		err := targetLock.Unlock()
		require.NoError(t, err)
	}()

	// 4. Check if "target" is in use again. It should now fail.
	err = EnsureInstanceNotInUse("target", inst.BaseDir, inst.Flavor.InstancesDirOpt)
	assert.Error(t, err, "target instance should be detected as in use")
}

func TestRemoveCachemanService_CommandArguments(t *testing.T) {
	baseDir, inst := setupMockInstances(t)
	targetInstance := "instance-to-delete"

	// Mock systemctl check so it proceeds to the removal command
	oldCheckSystemctl := checkSystemctlSupported
	checkSystemctlSupported = func() bool { return true }
	defer func() { checkSystemctlSupported = oldCheckSystemctl }()

	// Intercept exec.Command using our shared helper
	capturedCmds := mockExecCommand(t)

	err := removeCachemanService(targetInstance, inst.BaseDir, inst.Flavor.InstancesDirOpt)
	require.NoError(t, err)

	require.Len(t, *capturedCmds, 2)

	// 1st Command: Check if the systemd service exists.
	// It must query "cacheman@instance-to-delete.service", NOT "cacheman@default.service".
	assert.Equal(t, []string{
		"systemctl",
		"--user",
		"list-unit-files",
		"cacheman@instance-to-delete.service",
		"-q",
	}, (*capturedCmds)[0])

	// 2nd Command: Remove the service.
	// It must pass "--instance instance-to-delete", NOT "--instance default".
	abfsBin, _ := os.Executable()
	assert.Equal(t, []string{
		abfsBin,
		"--abfs-base-dir",
		baseDir,
		"--instance",
		"instance-to-delete",
		"cacheman",
		"remove",
	}, (*capturedCmds)[1])
}
