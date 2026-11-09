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

package client

import (
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDialPeer(t *testing.T) {
	cacheDir := t.TempDir()
	// The listener never accepts, like a stuck cacheman: the kernel still queues the
	// connection and names the listener.
	l, err := net.Listen("unix", HTTPSocketPath(cacheDir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	peer, err := DialPeer(cacheDir, time.Second)
	require.NoError(t, err)
	require.Equal(t, Peer{Pid: os.Getpid(), UID: os.Getuid()}, peer)
}

func TestDialPeer_NoSocket(t *testing.T) {
	_, err := DialPeer(t.TempDir(), time.Second)
	require.ErrorIs(t, err, syscall.ENOENT)
}

func TestDialPeer_Refused(t *testing.T) {
	cacheDir := t.TempDir()
	l, err := net.Listen("unix", HTTPSocketPath(cacheDir))
	require.NoError(t, err)
	// Leave the socket file behind, as a cacheman killed with SIGKILL does.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, l.Close())

	_, err = DialPeer(cacheDir, time.Second)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
}
