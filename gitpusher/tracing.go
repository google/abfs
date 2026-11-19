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
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/stats"

	pb "github.com/google/abfs/shared/remote/grpc/proto"
)

const tracerName = "github.com/google/abfs/gitpusher"

// skippedMethods are the per-object calls of a push. A push makes one per
// object, so they get no span and the push trace keeps one span per state.
var skippedMethods = map[string]bool{
	"/" + pb.ABFS_ServiceDesc.ServiceName + "/PutObject":    true,
	"/" + pb.ABFS_ServiceDesc.ServiceName + "/GetObject":    true,
	"/" + pb.ABFS_ServiceDesc.ServiceName + "/AddMapResult": true,
	"/" + pb.ABFS_ServiceDesc.ServiceName + "/MapResults":   true,
}

type skippedKey struct{}

// skipHandler hides the skipped methods from the inner trace handler. A filter
// on the inner handler is not enough: its TagRPC still sends the trace header,
// so the server would record a span per object. HandleRPC must skip them too,
// or the inner handler ends the span it finds in the context, the push span.
type skipHandler struct {
	inner stats.Handler
}

var _ stats.Handler = (*skipHandler)(nil)

func newSkipHandler(inner stats.Handler) *skipHandler {
	return &skipHandler{inner: inner}
}

// TagRPC implements stats.Handler.
func (h *skipHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	if skippedMethods[info.FullMethodName] {
		return context.WithValue(ctx, skippedKey{}, true)
	}
	return h.inner.TagRPC(ctx, info)
}

// HandleRPC implements stats.Handler.
func (h *skipHandler) HandleRPC(ctx context.Context, s stats.RPCStats) {
	if ctx.Value(skippedKey{}) != nil {
		return
	}
	h.inner.HandleRPC(ctx, s)
}

// TagConn implements stats.Handler.
func (h *skipHandler) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	return h.inner.TagConn(ctx, info)
}

// HandleConn implements stats.Handler.
func (h *skipHandler) HandleConn(ctx context.Context, s stats.ConnStats) {
	h.inner.HandleConn(ctx, s)
}

// setState moves the push to st. When the push span in ctx is recording, it
// ends the span of the previous state and starts one named after st.
func (s *pusher) setState(ctx context.Context, st PushState) {
	s.state = st
	s.endStateSpan(nil)
	if st == PushStateSleep || st == PushStateInitialized {
		return
	}
	push := trace.SpanFromContext(ctx)
	if !push.IsRecording() {
		return
	}
	_, s.phaseSpan = push.TracerProvider().Tracer(tracerName).Start(ctx, st.String())
	if s.remoteStats != nil {
		s.phaseWait, s.phaseSent = s.remoteStats()
	}
}

// endStateSpan ends the span of the current state, if any. The remote wait
// and bytes come from counters shared by all pushes of the process.
func (s *pusher) endStateSpan(err error) {
	if s.phaseSpan == nil {
		return
	}
	if s.remoteStats != nil {
		wait, sent := s.remoteStats()
		s.phaseSpan.SetAttributes(
			attribute.Float64("abfs.process.remote_wait_seconds", (wait-s.phaseWait).Seconds()),
			attribute.Int64("abfs.process.remote_bytes_sent", sent-s.phaseSent),
		)
	}
	endSpan(s.phaseSpan, err)
	s.phaseSpan = nil
}

// setStateAttributes adds attributes to the span of the current state.
func (s *pusher) setStateAttributes(kv ...attribute.KeyValue) {
	if s.phaseSpan != nil {
		s.phaseSpan.SetAttributes(kv...)
	}
}

// inSpan runs fn in a child span of the current state span.
func (s *pusher) inSpan(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	if s.phaseSpan == nil {
		return fn(ctx)
	}
	ctx, span := s.phaseSpan.TracerProvider().Tracer(tracerName).Start(trace.ContextWithSpan(ctx, s.phaseSpan), name)
	err := fn(ctx)
	endSpan(span, err)
	return err
}

func endSpan(span trace.Span, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

func (g *GitPusher) remoteStats() (time.Duration, int64) {
	var wait time.Duration
	var sent int64
	if g.limitedRemote != nil {
		wait = g.limitedRemote.WaitTime()
	}
	if g.abfsClient != nil {
		sent = g.abfsClient.Stat().BytesSent
	}
	return wait, sent
}
