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

package gitpusher

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc/filters"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	abfsclient "github.com/google/abfs/shared/remote/grpc/client"
	"github.com/google/abfs/shared/remote/grpc/server"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

func newTestTracerProvider() (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	rec := tracetest.NewSpanRecorder()
	return sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)), rec
}

func newTestClientHandler(tp *sdktrace.TracerProvider) stats.Handler {
	return newSkipHandler(otelgrpc.NewClientHandler(
		otelgrpc.WithTracerProvider(tp),
		otelgrpc.WithPropagators(propagation.TraceContext{}),
		otelgrpc.WithFilter(filters.None(filters.HealthCheck())),
	))
}

func TestSkipHandler_SkippedCallKeepsParent(t *testing.T) {
	require := require.New(t)
	tp, rec := newTestTracerProvider()
	h := newTestClientHandler(tp)

	ctx, push := tp.Tracer("test").Start(context.Background(), "push")
	for _, m := range []string{"PutObject", "GetObject", "AddMapResult", "MapResults"} {
		rpcCtx := h.TagRPC(ctx, &stats.RPCTagInfo{FullMethodName: "/abfs.ABFS/" + m})
		md, _ := metadata.FromOutgoingContext(rpcCtx)
		require.Empty(md.Get("traceparent"), "%s must not carry a trace header", m)
		h.HandleRPC(rpcCtx, &stats.Begin{Client: true, BeginTime: time.Now()})
		h.HandleRPC(rpcCtx, &stats.End{Client: true, EndTime: time.Now()})
		require.True(push.IsRecording(), "the end of a skipped %s must not end the push span", m)
	}
	require.Empty(rec.Ended(), "skipped calls make no spans")

	rpcCtx := h.TagRPC(ctx, &stats.RPCTagInfo{FullMethodName: "/abfs.ABFS/PutRefs"})
	md, _ := metadata.FromOutgoingContext(rpcCtx)
	require.Len(md.Get("traceparent"), 1)
	h.HandleRPC(rpcCtx, &stats.End{Client: true, EndTime: time.Now()})
	require.True(push.IsRecording())
	push.End()

	ended := rec.Ended()
	require.Len(ended, 2)
	require.Equal("abfs.ABFS/PutRefs", ended[0].Name())
	require.Equal(push.SpanContext().SpanID(), ended[0].Parent().SpanID())
}

// headerRecorder records the trace header of each call the server receives.
type headerRecorder struct {
	mu      sync.Mutex
	headers map[string][]string
}

func (r *headerRecorder) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headers[info.FullMethodName] = append(r.headers[info.FullMethodName], md.Get("traceparent")...)
	return ctx
}

func (r *headerRecorder) HandleRPC(context.Context, stats.RPCStats) {}

func (r *headerRecorder) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (r *headerRecorder) HandleConn(context.Context, stats.ConnStats) {}

func TestSkipHandler_ServerSeesNoTraceHeader(t *testing.T) {
	require := require.New(t)
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(err)
	backend := inmemory.New()
	rec := &headerRecorder{headers: map[string][]string{}}
	s, err := server.NewGRPCServer(&server.GRPCServerOpts{
		Server:          backend,
		ExtraServerOpts: []grpc.ServerOption{grpc.StatsHandler(rec)},
	})
	require.NoError(err)
	go func() { _ = s.Serve(lis) }()
	defer s.GracefulStop()

	tp, _ := newTestTracerProvider()
	c, err := abfsclient.NewABFSClient(&abfsclient.ABFSClientOpts{
		Servers:      []string{fmt.Sprintf("localhost:%d", lis.Addr().(*net.TCPAddr).Port)},
		AuthInfo:     instance.CreateAuthInfo(instance.AuthTypeNone, "", "", true),
		StatsHandler: newTestClientHandler(tp),
	})
	require.NoError(err)
	defer func() { _ = c.Close() }()

	ctx, push := tp.Tracer("test").Start(context.Background(), "push")
	defer push.End()
	h := entities.EmptyFileHash
	require.NoError(c.PutObject(ctx, h, remote.ContentReaderFromBytes(nil)))
	r, err := c.GetObject(ctx, h)
	require.NoError(err)
	require.NoError(r.Close())
	_, err = c.MapResults(ctx, h)
	require.NoError(err)
	require.NoError(c.PutRefs(ctx, "example.com", "repo", []entities.RefHash{{Ref: "refs/heads/main", Hash: h}}))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Empty(rec.headers["/abfs.ABFS/PutObject"])
	require.Empty(rec.headers["/abfs.ABFS/GetObject"])
	require.Empty(rec.headers["/abfs.ABFS/MapResults"])
	require.Len(rec.headers["/abfs.ABFS/PutRefs"], 1)
	require.Contains(rec.headers["/abfs.ABFS/PutRefs"][0], push.SpanContext().TraceID().String())
	require.Empty(rec.headers["/grpc.health.v1.Health/Check"], "health checks carry no trace header")
}

func TestSetState_PhaseSpans(t *testing.T) {
	require := require.New(t)
	tp, rec := newTestTracerProvider()
	var wait time.Duration
	p := &pusher{
		config:      &ProjectConfig{Server: "example.com", Project: "repo"},
		remoteStats: func() (time.Duration, int64) { return wait, 0 },
	}

	ctx, push := tp.Tracer("test").Start(context.Background(), "push")
	p.setState(ctx, PushStateFetch)
	p.setState(ctx, PushStateDiff)
	require.NoError(p.inSpan(ctx, "missing-hashes", func(context.Context) error { return nil }))
	p.setState(ctx, PushStatePushBlobs)
	wait = 2 * time.Second
	p.setState(ctx, PushStateSleep)
	require.Equal(PushStateSleep, p.state)
	push.End()

	ended := rec.Ended()
	var names []string
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range ended {
		names = append(names, s.Name())
		byName[s.Name()] = s
	}
	require.Equal([]string{"fetch", "missing-hashes", "diff", "push-blobs", "push"}, names)
	for _, n := range []string{"fetch", "diff", "push-blobs"} {
		require.Equal(push.SpanContext().SpanID(), byName[n].Parent().SpanID(), "%s is a child of the push span", n)
	}
	require.Equal(byName["diff"].SpanContext().SpanID(), byName["missing-hashes"].Parent().SpanID())
	var waited float64
	for _, kv := range byName["push-blobs"].Attributes() {
		if kv.Key == "abfs.process.remote_wait_seconds" {
			waited = kv.Value.AsFloat64()
		}
	}
	require.Equal(2.0, waited)
}

func TestSetState_NotRecording(t *testing.T) {
	require := require.New(t)
	p := &pusher{config: &ProjectConfig{Server: "example.com", Project: "repo"}}
	ctx := context.Background()
	p.setState(ctx, PushStateFetch)
	require.Nil(p.phaseSpan, "no state span without a recording push span")
	require.NoError(p.inSpan(ctx, "missing-hashes", func(context.Context) error { return nil }))
	p.setState(ctx, PushStateSleep)
	require.Equal(PushStateSleep, p.state)
}
