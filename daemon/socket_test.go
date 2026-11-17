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

package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNeedListener(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".abfs.sock")

	listen, err := NeedListener(p)
	require.NoError(t, err)
	require.True(t, listen, "no socket file")

	l, err := net.Listen("unix", p)
	require.NoError(t, err)
	listen, err = NeedListener(p)
	require.NoError(t, err)
	require.False(t, listen, "a server answers")

	// Closing a unix listener removes its file; a socket left behind by a process that died
	// stays.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, l.Close())
	require.FileExists(t, p)
	listen, err = NeedListener(p)
	require.NoError(t, err)
	require.True(t, listen, "nobody answers")
	require.NoFileExists(t, p, "the stale socket file is removed")

	require.NoError(t, os.WriteFile(p, nil, 0644))
	_, err = NeedListener(p)
	require.ErrorContains(t, err, "not a unix domain socket")
}
