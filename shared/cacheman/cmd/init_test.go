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

package cacheman

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/systemd"
)

func TestCheckUnitBaseDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	svc := systemd.Service{Name: "cacheman@test"}

	// No unit file yet.
	require.NoError(t, checkUnitBaseDir(svc, "/home/me/.abfs"))

	require.NoError(t, os.MkdirAll(filepath.Dir(svc.UnitFile()), 0700))
	writeUnit := func(execStart string) {
		unit := "[Unit]\nDescription=x\n\n[Service]\nExecStart=" + execStart + "\n"
		require.NoError(t, os.WriteFile(svc.UnitFile(), []byte(unit), 0600))
	}

	writeUnit("/bin/abfs --instance test --abfs-base-dir /home/me/.abfs cacheman run --logfile /l")
	require.NoError(t, checkUnitBaseDir(svc, "/home/me/.abfs"))
	err := checkUnitBaseDir(svc, "/tmp/scratch")
	require.ErrorContains(t, err, "already runs cacheman for base dir /home/me/.abfs, not /tmp/scratch")

	// The standalone cacheman binary uses --base-dir.
	writeUnit("/bin/cacheman --instance test --base-dir /home/me/.cacheman run --logfile /l")
	require.NoError(t, checkUnitBaseDir(svc, "/home/me/.cacheman"))
	require.Error(t, checkUnitBaseDir(svc, "/tmp/scratch"))

	// A changed binary path alone is fine.
	writeUnit("/usr/bin/abfs --instance test --abfs-base-dir /home/me/.abfs cacheman run --logfile /l")
	require.NoError(t, checkUnitBaseDir(svc, "/home/me/.abfs"))
}
