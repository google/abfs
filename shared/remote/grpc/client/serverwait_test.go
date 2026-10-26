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
	"crypto/rand"
	"fmt"
	"io"
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
	"github.com/google/abfs/shared/objects"
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

func TestServerWait_DownloadOutlastsRestart(t *testing.T) {
	require := require.New(t)
	srv := newRestartableServer(t, inmemory.New())
	c := srv.client(time.Minute)
	ctx := context.Background()
	data := make([]byte, 64<<20)
	_, err := rand.Read(data)
	require.NoError(err)
	h := objects.HashBlobBytes(data)
	require.NoError(c.PutObject(ctx, h, remote.NewBlobReaderFromBytes(data)))
	want := readObject(t, c, h)

	r, err := c.GetObject(ctx, h)
	require.NoError(err)
	defer r.Close()
	head := make([]byte, 1<<20)
	_, err = io.ReadFull(r, head)
	require.NoError(err)

	const outage = 9 * time.Second
	srv.stop()
	go func() {
		time.Sleep(outage)
		srv.start()
	}()
	rest, err := io.ReadAll(r)
	require.NoError(err)
	require.Equal(want, append(head, rest...))
}

func readObject(t *testing.T, c *ABFSClient, h remote.Hash) []byte {
	r, err := c.GetObject(context.Background(), h)
	require.NoError(t, err)
	defer r.Close()
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return b
}

func TestServerWait_UploadOutlastsRestart(t *testing.T) {
	require := require.New(t)
	srv := newRestartableServer(t, inmemory.New())
	c := srv.client(time.Minute)
	ctx := context.Background()
	data := make([]byte, 100<<10)
	_, err := rand.Read(data)
	require.NoError(err)
	h := objects.HashBlobBytes(data)

	const outage = 9 * time.Second
	srv.stop()
	// An upload on the connection that is being closed would be cut, not refused.
	conn := c.cm.conns[c.cm.hasher.Get(hashStr(h))]
	require.Eventually(func() bool { return conn.GetState() != connectivity.Ready }, 10*time.Second, 10*time.Millisecond)
	go func() {
		time.Sleep(outage)
		srv.start()
	}()

	start := time.Now()
	require.NoError(c.PutObject(ctx, h, remote.NewBlobReaderFromBytes(data)))
	require.GreaterOrEqual(time.Since(start), outage-time.Second)
	got := readObject(t, c, h)
	require.Equal(data, got[len(got)-len(data):])
}
