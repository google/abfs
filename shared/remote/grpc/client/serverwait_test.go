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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/grpc/server"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

// restartableServer serves one backend on a fixed port and can be stopped and started again.
type restartableServer struct {
	t       *testing.T
	backend remote.RemoteCloser
	port    int
	s       *grpc.Server
}

func newRestartableServer(t *testing.T, backend remote.RemoteCloser) *restartableServer {
	r := &restartableServer{t: t, backend: backend}
	r.start()
	t.Cleanup(r.stop)
	return r
}

func (r *restartableServer) start() {
	lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", r.port))
	require.NoError(r.t, err)
	r.port = lis.Addr().(*net.TCPAddr).Port
	s, err := server.NewGRPCServer(&server.GRPCServerOpts{Server: r.backend})
	require.NoError(r.t, err)
	r.s = s
	go func() { _ = s.Serve(lis) }()
}

func (r *restartableServer) stop() {
	if r.s != nil {
		r.s.Stop()
		r.s = nil
	}
}

func (r *restartableServer) client(wait time.Duration) *ABFSClient {
	c, err := NewABFSClient(&ABFSClientOpts{
		Servers:           []string{fmt.Sprintf("localhost:%d", r.port)},
		AuthInfo:          instance.CreateAuthInfo(instance.AuthTypeNone, "", "", true),
		ServerWaitTimeout: wait,
	})
	require.NoError(r.t, err)
	r.t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestServerWait_FailsAtOnceAfterTimeout(t *testing.T) {
	require := require.New(t)
	srv := newRestartableServer(t, inmemory.New())
	c := srv.client(time.Second)
	ctx := context.Background()
	h := entities.NewHash("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391")
	_, err := c.Headers(ctx, []remote.Hash{h})
	require.NoError(err)

	srv.stop()
	_, err = c.Headers(ctx, []remote.Hash{h})
	require.ErrorContains(err, "server unreachable for")
	conn := c.cm.conns[c.cm.hasher.Get(hashStr(h))]
	require.Eventually(func() bool { return conn.GetState() == connectivity.TransientFailure }, 10*time.Second, 10*time.Millisecond)

	// The timeout has passed and gRPC knows the server is down: no new attempt.
	start := time.Now()
	_, err = c.Headers(ctx, []remote.Hash{h})
	require.Equal(codes.Unavailable, status.Code(err))
	require.ErrorContains(err, "transient failure")
	require.Less(time.Since(start), time.Second)
}

func TestServerWait_UnaryCallOutlastsRestart(t *testing.T) {
	require := require.New(t)
	srv := newRestartableServer(t, inmemory.New())
	c := srv.client(time.Minute)
	ctx := context.Background()
	h := entities.NewHash("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391")
	_, err := c.Headers(ctx, []remote.Hash{h})
	require.NoError(err)

	// Longer than gRPC's own retry policy (5 attempts, about 7.5 s of backoff).
	const outage = 9 * time.Second
	srv.stop()
	go func() {
		time.Sleep(outage)
		srv.start()
	}()

	start := time.Now()
	_, err = c.Headers(ctx, []remote.Hash{h})
	require.NoError(err)
	require.GreaterOrEqual(time.Since(start), outage-time.Second)
}
