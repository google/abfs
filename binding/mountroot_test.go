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

package binding

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

func TestMountRootForSocket(t *testing.T) {
	mnt := t.TempDir()
	top := filepath.Join(mnt, "android")
	require.NoError(t, os.MkdirAll(filepath.Join(top, "frameworks"), 0755))
	l, err := net.Listen("unix", filepath.Join(top, entities.ABFSSocketFileName))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	socketDir, err := FindRootForSocket(filepath.Join(top, "frameworks"), entities.ABFSSocketFileName)
	require.NoError(t, err)
	require.Equal(t, top, socketDir, "the socket of the top is found first")

	// The test tree is no mount, so the walk ends at the file system that holds it.
	want, err := bindcas.MountPoint(mnt)
	require.NoError(t, err)
	root, err := MountRootForSocket(filepath.Join(top, "frameworks"), entities.ABFSSocketFileName)
	require.NoError(t, err)
	require.Equal(t, want, root)
	require.NotEqual(t, top, root)
}

func TestMountRoot_SocketAtTheMountPoint(t *testing.T) {
	mountPoint, err := bindcas.MountPoint(t.TempDir())
	require.NoError(t, err)
	require.Equal(t, mountPoint, mountRoot(mountPoint), "a socket at the mount point keeps its directory as the root")
}
