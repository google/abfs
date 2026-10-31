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
	"fmt"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc/filters"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc/stats"
)

const (
	// FirstPushKey is set on the span of a git-pusher project push. The
	// default sampler starts traces only for spans that carry it.
	FirstPushKey = attribute.Key("abfs.push.first")

	defaultPushRatio = 0.01
	setupTimeout     = 10 * time.Second
)

// samplerNames are the OTEL_TRACES_SAMPLER values that the SDK knows.
var samplerNames = []string{
	"always_on",
	"always_off",
	"traceidratio",
	"parentbased_always_on",
	"parentbased_always_off",
	"parentbased_traceidratio",
}

var (
	resOnce     sync.Once
	res         *resource.Resource
	errResource error

	// tracerProvider is nil unless StartTracing built one. It is passed to the
	// gRPC handlers explicitly and never set as the global provider, so client
	// libraries that use the global one stay untraced.
	tracerProvider *sdktrace.TracerProvider
)

// Resource returns the resource built by NewResource, once per process, so
// metrics and traces share it.
func Resource(ctx context.Context) (*resource.Resource, error) {
	resOnce.Do(func() {
		res, errResource = NewResource(ctx)
	})
	return res, errResource
}

// TracingEnabled reports whether OTEL_TRACES_EXPORTER asks for traces.
func TracingEnabled() bool {
	v := strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER"))
	return v != "" && v != "none"
}

// StartTracing builds the tracer provider when TracingEnabled and returns the
// function that flushes and stops it. Call it after metrics.SetInstanceName
// and before any gRPC dial.
func StartTracing(ctx context.Context) (func(context.Context) error, error) {
	if !TracingEnabled() {
		return func(context.Context) error { return nil }, nil
	}
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()

	r, err := Resource(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build the resource: %w", err)
	}
	exporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create the span exporter: %w", err)
	}
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(r),
	}
	// The SDK reads OTEL_TRACES_SAMPLER, but WithSampler would override it. For a
	// name it does not know the SDK samples every trace, so that gets the default.
	if name := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER"))); !slices.Contains(samplerNames, name) {
		if name != "" {
			log.Printf("Warning: traces: unknown OTEL_TRACES_SAMPLER %q, using the default sampler", name)
		}
		opts = append(opts, sdktrace.WithSampler(defaultSampler()))
	}
	tracerProvider = sdktrace.NewTracerProvider(opts...)
	return tracerProvider.Shutdown, nil
}

// Tracer returns a tracer of the process provider, or a no-op tracer when
// tracing is off.
func Tracer(name string) trace.Tracer {
	if tracerProvider == nil {
		return tracenoop.NewTracerProvider().Tracer(name)
	}
	return tracerProvider.Tracer(name)
}

// ServerHandler returns the gRPC server stats handler for traces, or nil when
// tracing is off.
func ServerHandler() stats.Handler {
	if tracerProvider == nil {
		return nil
	}
	return otelgrpc.NewServerHandler(grpcOptions(tracerProvider)...)
}

// ClientHandler returns the gRPC client stats handler for traces, or nil when
// tracing is off.
func ClientHandler() stats.Handler {
	if tracerProvider == nil {
		return nil
	}
	return otelgrpc.NewClientHandler(grpcOptions(tracerProvider)...)
}

func grpcOptions(tp trace.TracerProvider) []otelgrpc.Option {
	return []otelgrpc.Option{
		otelgrpc.WithTracerProvider(tp),
		// The server has its own RPC metrics.
		otelgrpc.WithMeterProvider(metricnoop.NewMeterProvider()),
		otelgrpc.WithPropagators(propagation.TraceContext{}),
		otelgrpc.WithFilter(filters.None(filters.HealthCheck(), filters.ServicePrefix("grpc.reflection."))),
	}
}

// defaultSampler follows a sampled parent and starts traces only for project
// pushes: every first push, the others at OTEL_TRACES_SAMPLER_ARG. Processes
// other than git-pusher start none, so they trace only what a caller traces.
func defaultSampler() sdktrace.Sampler {
	return sdktrace.ParentBased(pushSampler{ratio: sdktrace.TraceIDRatioBased(pushRatio())})
}

// pushRatio reads OTEL_TRACES_SAMPLER_ARG, which the SDK reads only together
// with OTEL_TRACES_SAMPLER.
func pushRatio() float64 {
	v := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG"))
	if v == "" {
		return defaultPushRatio
	}
	ratio, err := strconv.ParseFloat(v, 64)
	if err != nil || ratio < 0 || ratio > 1 {
		log.Printf("Warning: traces: OTEL_TRACES_SAMPLER_ARG %q is not a ratio between 0 and 1, using %v", v, defaultPushRatio)
		return defaultPushRatio
	}
	return ratio
}

type pushSampler struct {
	ratio sdktrace.Sampler
}

var _ sdktrace.Sampler = pushSampler{}

// ShouldSample implements sdktrace.Sampler.
func (s pushSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	for _, kv := range p.Attributes {
		if kv.Key != FirstPushKey {
			continue
		}
		if kv.Value.AsBool() {
			return sdktrace.AlwaysSample().ShouldSample(p)
		}
		return s.ratio.ShouldSample(p)
	}
	return sdktrace.NeverSample().ShouldSample(p)
}

// Description implements sdktrace.Sampler.
func (s pushSampler) Description() string {
	return fmt.Sprintf("PushSampler{%s}", s.ratio.Description())
}
