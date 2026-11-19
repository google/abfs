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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"

	pb "github.com/google/abfs/ares/grpc/proto"
)

// TestNewGRPCServer_KeepalivePings checks that a client pinging every 10 s, the most often a
// grpc-go client can, keeps a call open on which the server writes nothing for 45 s. With
// grpc-go's default policy the server closes such a connection after about 30 to 40 s.
func TestNewGRPCServer_KeepalivePings(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := NewGRPCServer(nil, 0)
	go func() {
		_ = s.Serve(lis)
	}()
	defer s.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 10 * time.Second, Timeout: 5 * time.Second}),
	)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Watch sends the current status, then nothing until it changes.
	stream, err := grpc_health_v1.NewHealthClient(conn).Watch(ctx, &grpc_health_v1.HealthCheckRequest{Service: pb.ARES_ServiceDesc.ServiceName})
	require.NoError(t, err)
	resp, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, resp.Status)

	errc := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		errc <- err
	}()
	select {
	case err := <-errc:
		require.Fail(t, "call ended while the server was idle", "%v", err)
	case <-time.After(45 * time.Second):
	}
}
