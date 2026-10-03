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

package grpc

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

type mockTokenSource struct{}

func (m mockTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{
		AccessToken: "mock-token",
		TokenType:   "Bearer",
	}, nil
}

func TestGrpcDialHealthy_DisableTLS(t *testing.T) {
	// 1. Start a local non-TLS gRPC server with Health service
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = lis.Close() }()

	s := grpc.NewServer()
	hs := health.NewServer()
	serviceName := "test-service"
	hs.SetServingStatus(serviceName, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(s, hs)

	go func() {
		_ = s.Serve(lis)
	}()
	defer s.GracefulStop()

	// 2. Try to connect with TokenSource and DisableTLS: true (should succeed)
	opts := &GrpcDialOpts{
		ServiceName:      serviceName,
		Address:          lis.Addr().String(),
		BytesStatHandler: mockBytesStatHandler{},
		ClientVersion:    "0.1.2",
		ServerMinVersion: "0.1.1",
		TokenSource:      mockTokenSource{},
		DisableTLS:       true,
	}

	conn, err := GrpcDialHealthy(opts)
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.NoError(t, conn.Close())

	// 3. Try to connect with TokenSource and DisableTLS: false (should fail because server is non-TLS)
	optsFail := &GrpcDialOpts{
		ServiceName:      serviceName,
		Address:          lis.Addr().String(),
		BytesStatHandler: mockBytesStatHandler{},
		ClientVersion:    "0.1.2",
		ServerMinVersion: "0.1.1",
		TokenSource:      mockTokenSource{},
		DisableTLS:       false,
	}

	_, err = GrpcDialHealthy(optsFail)
	require.Error(t, err)
}

func TestGrpcDialHealthy_TLSWithoutTokenSource(t *testing.T) {
	// 1. Start a local non-TLS gRPC server with Health service
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = lis.Close() }()

	s := grpc.NewServer()
	hs := health.NewServer()
	serviceName := "test-service"
	hs.SetServingStatus(serviceName, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(s, hs)

	go func() {
		_ = s.Serve(lis)
	}()
	defer s.GracefulStop()

	// 2. Try to connect with TokenSource: nil and DisableTLS: false
	// Since the server is non-TLS, the client SHOULD attempt TLS and fail.
	opts := &GrpcDialOpts{
		ServiceName:      serviceName,
		Address:          lis.Addr().String(),
		BytesStatHandler: mockBytesStatHandler{},
		ClientVersion:    "0.1.2",
		ServerMinVersion: "0.1.1",
		TokenSource:      nil,
		DisableTLS:       false,
	}

	_, err = GrpcDialHealthy(opts)
	require.Error(t, err, "expected connection to fail because client uses TLS but server is non-TLS")
}

type mockBytesStatHandler struct{}

func (m mockBytesStatHandler) AddBytesSent(int64)     {}
func (m mockBytesStatHandler) AddBytesReceived(int64) {}
