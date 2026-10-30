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

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/google/abfs/shared/cmd/root"
)

const waitMethod = "/test.Slow/Wait"

// receive returns the next value from ch, or fails the test after 10s.
func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// newSlowServer returns a server with one unary method that signals started
// and then blocks until release is closed or the RPC is canceled, plus a client
// connected to it.
func newSlowServer(t *testing.T, started chan<- struct{}, release <-chan struct{}) (*grpc.Server, net.Listener, *grpc.ClientConn) {
	s := grpc.NewServer()
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "test.Slow",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Wait",
			Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				if err := dec(&emptypb.Empty{}); err != nil {
					return nil, err
				}
				started <- struct{}{}
				select {
				case <-release:
					return &emptypb.Empty{}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
		}},
	}, struct{}{})

	lis := bufconn.Listen(1024 * 1024)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}))
	require.NoError(t, err, "error creating client")
	t.Cleanup(func() {
		require.NoError(t, conn.Close(), "error closing client")
	})
	return s, lis, conn
}

func TestServeGRPC_GracefulStop(t *testing.T) {
	require := require.New(t)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s, lis, conn := newSlowServer(t, started, release)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- serveGRPC(ctx, s, lis, time.Minute)
	}()

	called := make(chan error, 1)
	go func() {
		called <- conn.Invoke(context.Background(), waitMethod, &emptypb.Empty{}, &emptypb.Empty{})
	}()
	receive(t, started, "the RPC to start")
	cancel()

	select {
	case err := <-served:
		t.Fatalf("serveGRPC returned while an RPC was pending: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	require.NoError(receive(t, called, "the pending RPC"), "error from the pending RPC")
	require.NoError(receive(t, served, "serveGRPC"), "error from serveGRPC")
}

func TestServeGRPC_Timeout(t *testing.T) {
	require := require.New(t)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	s, lis, conn := newSlowServer(t, started, release)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- serveGRPC(ctx, s, lis, 100*time.Millisecond)
	}()

	called := make(chan error, 1)
	go func() {
		called <- conn.Invoke(context.Background(), waitMethod, &emptypb.Empty{}, &emptypb.Empty{})
	}()
	receive(t, started, "the RPC to start")
	cancel()

	require.NoError(receive(t, served, "serveGRPC"), "error from serveGRPC")
	require.Equal(codes.Unavailable, status.Code(receive(t, called, "the pending RPC")))
}

func TestServeGRPC_CanceledBeforeServe(t *testing.T) {
	s := grpc.NewServer()
	lis := bufconn.Listen(1024)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, serveGRPC(ctx, s, lis, time.Minute))
}

func TestServeGRPC_ServeError(t *testing.T) {
	s := grpc.NewServer()
	lis := bufconn.Listen(1024)
	require.NoError(t, lis.Close(), "error closing listener")

	err := serveGRPC(context.Background(), s, lis, time.Minute)
	require.Error(t, err)
}

func TestServerCmd_ShutdownTimeoutDefault(t *testing.T) {
	c := NewCmd(root.NewRootCommand())
	fl := pflag.NewFlagSet("server", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	require.NoError(t, fl.Parse(nil))
	require.Equal(t, 25*time.Second, c.shutdownTimeout)
}
