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
	"context"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/instance"
	abfsgrpc "github.com/google/abfs/shared/remote/grpc"
)

// blackHoledAddr returns an address that accepts no connections and answers no packets: a
// listener that never accepts, with its accept queue full.
func blackHoledAddr(t *testing.T) string {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Close(fd) })
	require.NoError(t, syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}))
	require.NoError(t, syscall.Listen(fd, 1))
	sa, err := syscall.Getsockname(fd)
	require.NoError(t, err)
	addr := fmt.Sprintf("127.0.0.1:%d", sa.(*syscall.SockaddrInet4).Port)
	for range 3 {
		if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			t.Cleanup(func() { _ = conn.Close() })
		}
	}
	return addr
}

func TestServerWait_BlackHoledServerStartsClock(t *testing.T) {
	require := require.New(t)
	c, err := NewABFSClient(&ABFSClientOpts{
		Servers:           []string{blackHoledAddr(t)},
		AuthInfo:          instance.CreateAuthInfo(instance.AuthTypeNone, "", "", true),
		SkipHealthCheck:   true,
		ServerWaitTimeout: time.Minute,
	})
	require.NoError(err)
	defer c.Close()

	// A health check with a short deadline ends while gRPC is still connecting.
	ctx, cancel := context.WithTimeout(abfsgrpc.WithoutServerWait(context.Background()), time.Second)
	defer cancel()
	_, err = c.Version(ctx)
	require.Equal(codes.DeadlineExceeded, status.Code(err))
	require.True(c.WaitingForServer())
}
