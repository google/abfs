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

package telemetry

import (
	"context"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func resetTracing(t *testing.T) {
	t.Cleanup(func() {
		tracerProvider = nil
		resOnce = sync.Once{}
		res, errResource = nil, nil
	})
}

func TestStartTracing_Off(t *testing.T) {
	for _, v := range []string{"", "none", " none "} {
		t.Run("exporter="+v, func(t *testing.T) {
			require := require.New(t)
			resetTracing(t)
			t.Setenv("OTEL_TRACES_EXPORTER", v)
			before := runtime.NumGoroutine()

			shutdown, err := StartTracing(context.Background())
			require.NoError(err)
			require.Nil(tracerProvider)
			require.Nil(ServerHandler())
			require.Nil(ClientHandler())
			require.Nil(res, "no resource detection with tracing off")
			require.NoError(errResource)
			require.Equal(before, runtime.NumGoroutine(), "no goroutines with tracing off")
			_, span := Tracer("test").Start(context.Background(), "span")
			require.False(span.IsRecording())
			require.NoError(shutdown(context.Background()))
		})
	}
}

func TestStartTracing_On(t *testing.T) {
	require := require.New(t)
	resetTracing(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	t.Setenv("OTEL_TRACES_SAMPLER", "")

	shutdown, err := StartTracing(context.Background())
	require.NoError(err)
	require.NotNil(tracerProvider)
	require.NotNil(ServerHandler())
	require.NotNil(ClientHandler())

	tracer := Tracer("test")
	_, span := tracer.Start(context.Background(), "root")
	require.False(span.IsRecording(), "the default sampler starts no root without the push attribute")
	span.End()
	_, span = tracer.Start(context.Background(), "push", trace.WithAttributes(FirstPushKey.Bool(true)))
	require.True(span.IsRecording(), "a first push is always sampled")
	span.End()
	require.NoError(shutdown(context.Background()))
}

func TestStartTracing_SamplerFromEnvironment(t *testing.T) {
	require := require.New(t)
	resetTracing(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")

	shutdown, err := StartTracing(context.Background())
	require.NoError(err)
	_, span := Tracer("test").Start(context.Background(), "root")
	require.True(span.IsRecording(), "OTEL_TRACES_SAMPLER must replace the default sampler")
	span.End()
	require.NoError(shutdown(context.Background()))
}

func TestStartTracing_UnknownSampler(t *testing.T) {
	require := require.New(t)
	resetTracing(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_always_of")

	shutdown, err := StartTracing(context.Background())
	require.NoError(err)
	_, span := Tracer("test").Start(context.Background(), "root")
	require.False(span.IsRecording(), "an unknown OTEL_TRACES_SAMPLER must keep the default sampler")
	span.End()
	require.NoError(shutdown(context.Background()))
}

func TestDefaultSampler(t *testing.T) {
	sampledParent := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{1},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	}))
	unsampledParent := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1},
		SpanID:  trace.SpanID{1},
		Remote:  true,
	}))
	tests := []struct {
		name  string
		arg   string
		ctx   context.Context
		attrs []attribute.KeyValue
		want  bool
	}{
		{name: "root", ctx: context.Background(), want: false},
		{name: "first push", ctx: context.Background(), attrs: []attribute.KeyValue{FirstPushKey.Bool(true)}, want: true},
		{name: "re-push at ratio 1", arg: "1", ctx: context.Background(), attrs: []attribute.KeyValue{FirstPushKey.Bool(false)}, want: true},
		{name: "re-push at ratio 0", arg: "0", ctx: context.Background(), attrs: []attribute.KeyValue{FirstPushKey.Bool(false)}, want: false},
		{name: "sampled parent", ctx: sampledParent, want: true},
		{name: "unsampled parent", ctx: unsampledParent, attrs: []attribute.KeyValue{FirstPushKey.Bool(true)}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", tt.arg)
			tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(defaultSampler()))
			_, span := tp.Tracer("test").Start(tt.ctx, "span", trace.WithAttributes(tt.attrs...))
			require.Equal(t, tt.want, span.IsRecording())
		})
	}
}

func TestPushRatio(t *testing.T) {
	tests := []struct {
		arg  string
		want float64
	}{
		{"", defaultPushRatio},
		{"0.5", 0.5},
		{" 1 ", 1},
		{"0", 0},
		{"2", defaultPushRatio},
		{"-1", defaultPushRatio},
		{"x", defaultPushRatio},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", tt.arg)
			require.Equal(t, tt.want, pushRatio())
		})
	}
}
