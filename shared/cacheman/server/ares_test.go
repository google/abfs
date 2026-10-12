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
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	aresclient "github.com/google/abfs/ares/grpc/client"
	pb "github.com/google/abfs/ares/grpc/proto"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
)

// newHealthServer starts a gRPC server that only exposes the health service, reporting status for
// the ARES service. It returns the address the server is listening on.
func newHealthServer(t *testing.T, status grpc_health_v1.HealthCheckResponse_ServingStatus) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := grpc.NewServer()
	hs := health.NewServer()
	hs.SetServingStatus(pb.ARES_ServiceDesc.ServiceName, status)
	grpc_health_v1.RegisterHealthServer(s, hs)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	return lis.Addr().String()
}

// downAddress returns an address that nothing is listening on.
func downAddress(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	return addr
}

func TestSetupARESClientHealthCheck(t *testing.T) {
	newARES := func(address string, authInfo instance.AuthInfo) (remote.ARESService, error) {
		return aresclient.NewARESClient(address, authInfo)
	}

	tests := []struct {
		name        string
		address     func(t *testing.T) string
		wantSupport bool
	}{
		{
			name:        "ARES down",
			address:     downAddress,
			wantSupport: false,
		},
		{
			name: "ARES not serving",
			address: func(t *testing.T) string {
				return newHealthServer(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING)
			},
			wantSupport: false,
		},
		{
			name: "ARES serving",
			address: func(t *testing.T) string {
				return newHealthServer(t, grpc_health_v1.HealthCheckResponse_SERVING)
			},
			wantSupport: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &CacheMan{printer: new(DefaultPrinter)}
			config := &instance.Config{
				RemoteServers: []string{tc.address(t)},
				AuthType:      instance.AuthTypeNone,
				DisableTLS:    true,
			}

			require.NoError(t, c.setupARESClient(config, newARES, true))
			t.Cleanup(func() { _ = c.aresClient.Close() })
			require.Equal(t, tc.wantSupport, c.aresClient.Support())
		})
	}
}
