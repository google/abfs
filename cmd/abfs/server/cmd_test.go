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
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/remote/grpc/server"
	"github.com/google/abfs/shared/remote/servers/inmemory"
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
		served <- serveGRPC(ctx, s, nil, lis, 0, time.Minute)
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
		served <- serveGRPC(ctx, s, nil, lis, 0, 100*time.Millisecond)
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

	require.NoError(t, serveGRPC(ctx, s, nil, lis, 0, time.Minute))
}

func TestServeGRPC_ServeError(t *testing.T) {
	s := grpc.NewServer()
	lis := bufconn.Listen(1024)
	require.NoError(t, lis.Close(), "error closing listener")

	err := serveGRPC(context.Background(), s, nil, lis, 0, time.Minute)
	require.Error(t, err)
}

func TestServeGRPC_DrainDelay(t *testing.T) {
	require := require.New(t)

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	close(release)
	s, lis, conn := newSlowServer(t, started, release)
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(s, hs)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- serveGRPC(ctx, s, hs, lis, time.Second, time.Minute)
	}()
	hc := grpc_health_v1.NewHealthClient(conn)
	resp, err := hc.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	require.NoError(err)
	require.Equal(grpc_health_v1.HealthCheckResponse_SERVING, resp.Status)

	start := time.Now()
	cancel()
	// Health checks report the shutdown at once, while new requests are still served.
	require.Eventually(func() bool {
		resp, err := hc.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
		return err == nil && resp.Status == grpc_health_v1.HealthCheckResponse_NOT_SERVING
	}, 500*time.Millisecond, 10*time.Millisecond)
	require.NoError(conn.Invoke(context.Background(), waitMethod, &emptypb.Empty{}, &emptypb.Empty{}))

	require.NoError(receive(t, served, "serveGRPC"), "error from serveGRPC")
	require.GreaterOrEqual(time.Since(start), time.Second)
}

func TestServeGRPC_ServeErrorSkipsDrainDelay(t *testing.T) {
	s := grpc.NewServer()
	lis := bufconn.Listen(1024)
	require.NoError(t, lis.Close(), "error closing listener")

	start := time.Now()
	require.Error(t, serveGRPC(context.Background(), s, health.NewServer(), lis, time.Minute, time.Minute))
	require.Less(t, time.Since(start), 10*time.Second)
}

func TestServeGRPC_NotServingWithoutDrainDelay(t *testing.T) {
	s := grpc.NewServer()
	hs := health.NewServer()
	lis := bufconn.Listen(1024)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, serveGRPC(ctx, s, hs, lis, 0, time.Minute))
	resp, err := hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING, resp.Status)
}

func TestServerCmd_ShutdownTimeoutDefault(t *testing.T) {
	c := NewCmd(root.NewRootCommand())
	fl := pflag.NewFlagSet("server", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	require.NoError(t, fl.Parse(nil))
	require.Equal(t, 25*time.Second, c.shutdownTimeout)
	require.Zero(t, c.drainDelay)
}

func TestServeGRPC_HealthProbeKeepsDrain(t *testing.T) {
	require := require.New(t)

	s := grpc.NewServer()
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(s, hs)
	lis := bufconn.Listen(1024)
	// The marker shows when the drain has shut the health service down.
	const marker = "test.Marker"
	hs.SetServingStatus(marker, grpc_health_v1.HealthCheckResponse_SERVING)
	healthOf := func(service string) grpc_health_v1.HealthCheckResponse_ServingStatus {
		// Eventually calls this on another goroutine, so it cannot fail the test.
		resp, _ := hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{Service: service})
		return resp.GetStatus()
	}

	// The probe keeps running through the drain, which is the worst case: in
	// Serve it stops with the same signal.
	var pass atomic.Bool
	var calls atomic.Int32
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	defer cancelProbe()
	go func() {
		if err := server.RunHealthProbe(probeCtx, &server.HealthProbeOpts{
			Health: hs,
			Probes: map[string]func(context.Context) error{
				"store": func(context.Context) error {
					calls.Add(1)
					if pass.Load() {
						return nil
					}
					return errors.New("backend down")
				},
			},
			Interval: 10 * time.Millisecond,
			Timeout:  10 * time.Millisecond,
			Failures: 1,
		}); err != nil {
			t.Error(err)
		}
	}()
	require.Eventually(func() bool { return healthOf("") == grpc_health_v1.HealthCheckResponse_NOT_SERVING }, 10*time.Second, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- serveGRPC(ctx, s, hs, lis, 10*time.Second, time.Minute)
	}()
	cancel()
	require.Eventually(func() bool { return healthOf(marker) == grpc_health_v1.HealthCheckResponse_NOT_SERVING }, 10*time.Second, time.Millisecond)

	// Passing probes during the drain must not report SERVING again.
	pass.Store(true)
	n := calls.Load()
	require.Eventually(func() bool { return calls.Load() >= n+3 }, 10*time.Second, time.Millisecond)
	require.Equal(grpc_health_v1.HealthCheckResponse_NOT_SERVING, healthOf(""))
	require.Equal(grpc_health_v1.HealthCheckResponse_NOT_SERVING, healthOf("abfs.ABFS"))
	select {
	case err := <-served:
		t.Fatalf("serveGRPC returned during the drain: %v", err)
	default:
	}
	s.Stop()
}

func TestServerCmd_HealthProbeDefaults(t *testing.T) {
	c := NewCmd(root.NewRootCommand())
	fl := pflag.NewFlagSet("server", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	require.NoError(t, fl.Parse(nil))
	require.Zero(t, c.probeInterval)
	require.Equal(t, 5*time.Second, c.probeTimeout)
	require.Equal(t, 3, c.probeFailures)
}

func TestServerCmd_ServeRejectsHealthProbeFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "negative interval", args: []string{"--health-probe-interval=-1s"}, wantErr: "--health-probe-interval"},
		{name: "timeout above interval", args: []string{"--health-probe-interval=1s", "--health-probe-timeout=2s"}, wantErr: "--health-probe-timeout"},
		{name: "zero timeout", args: []string{"--health-probe-interval=1s", "--health-probe-timeout=0"}, wantErr: "--health-probe-timeout"},
		{name: "no failures", args: []string{"--health-probe-interval=10s", "--health-probe-failures=0"}, wantErr: "--health-probe-failures"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			// Serve must release the port when it rejects the flags.
			lis, err := net.Listen("tcp", ":0")
			require.NoError(err)
			port := strconv.Itoa(lis.Addr().(*net.TCPAddr).Port)
			require.NoError(lis.Close())

			c := NewCmd(root.NewRootCommand())
			fl := pflag.NewFlagSet("server", pflag.ContinueOnError)
			c.RegisterFlags(fl)
			require.NoError(fl.Parse(append([]string{"--port=" + port}, tt.args...)))
			require.ErrorContains(c.Serve(context.Background(), inmemory.New()), tt.wantErr)

			lis, err = net.Listen("tcp", ":"+port)
			require.NoError(err, "the port is still bound")
			require.NoError(lis.Close())
		})
	}
}
