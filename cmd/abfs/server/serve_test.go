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

package server

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

type closeCountingServer struct {
	remote.RemoteCloser
	closes atomic.Int32
}

func (s *closeCountingServer) Close() error {
	s.closes.Add(1)
	return nil
}

func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := lis.Addr().(*net.TCPAddr).Port
	require.NoError(t, lis.Close())
	return port
}

func nonLoopbackIP(t *testing.T) net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	require.NoError(t, err)
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP
		}
	}
	t.Skip("no non-loopback IPv4 address")
	return nil
}

// serveInMemory runs Serve with flags until the test ends and returns the
// backend it serves.
func serveInMemory(t *testing.T, args ...string) *closeCountingServer {
	t.Helper()
	c := NewCmd(root.NewRootCommand())
	fl := pflag.NewFlagSet("server", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	require.NoError(t, fl.Parse(append([]string{"--port", "0", "--no-logging"}, args...)))

	backend := &closeCountingServer{RemoteCloser: inmemory.New()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Serve(ctx, backend) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return")
		}
	})
	return backend
}

func TestServe_HTTPListensOnLocalhostByDefault(t *testing.T) {
	ip := nonLoopbackIP(t)
	port := freePort(t)
	serveInMemory(t, "--http-port", strconv.Itoa(port))

	require.Eventually(t, func() bool {
		conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			_ = conn.Close()
		}
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)), time.Second)
	if err == nil {
		_ = conn.Close()
	}
	require.Error(t, err, "the http server answers on %s", ip)
}

func TestServe_LeavesBackendOpen(t *testing.T) {
	var backend *closeCountingServer
	t.Run("serve", func(t *testing.T) {
		backend = serveInMemory(t)
		time.Sleep(100 * time.Millisecond)
	})
	// The creator of the backend closes it after Serve returns.
	require.Zero(t, backend.closes.Load())
}
