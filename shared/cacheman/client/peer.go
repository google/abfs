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
	"fmt"
	"net"
	"path"
	"time"

	"golang.org/x/sys/unix"
)

// Peer is the process that listens on a cacheman socket, as the kernel reports it.
type Peer struct {
	// Pid is 0 when the process is not visible in this pid namespace.
	Pid int
	UID int
}

// HTTPSocketPath returns the socket that the HTTP client dials for cacheDir.
func HTTPSocketPath(cacheDir string) string {
	return path.Join(cacheDir, "http.socket")
}

// DialPeer connects to the cacheman socket in cacheDir and returns the process
// that listens on it. It needs no answer from cacheman, so it also finds a
// cacheman that is stuck.
func DialPeer(cacheDir string, timeout time.Duration) (Peer, error) {
	conn, err := net.DialTimeout("unix", HTTPSocketPath(cacheDir), timeout)
	if err != nil {
		return Peer{}, err
	}
	defer func() { _ = conn.Close() }()

	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if credErr != nil {
		return Peer{}, fmt.Errorf("failed to read the peer of %s: %w", conn.RemoteAddr(), credErr)
	}
	return Peer{Pid: int(cred.Pid), UID: int(cred.Uid)}, nil
}
