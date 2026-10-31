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

// Package otelmetrics registers an OpenTelemetry backend for shared/metrics.
//
// Select it with the metrics-provider config key set to "otel". Exporters are
// configured with the standard OTEL_* environment variables, for example
// OTEL_METRICS_EXPORTER=otlp|prometheus|console|none.
//
// The otlp exporter spaces the final export at exit at least 5 s from the
// previous one, as some backends require, ignores Push, and does not support
// OTEL_METRICS_PRODUCERS.
package otelmetrics

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/google/abfs/shared/metrics"
	"github.com/google/abfs/shared/telemetry"
)

const (
	ProviderName = "otel"
	meterName    = "github.com/google/abfs"
	setupTimeout = 10 * time.Second
)

func init() {
	metrics.Register(ProviderName, newProvider)
}

func newProvider(enabled bool) metrics.Provider {
	if !enabled {
		return metrics.NewNoopProvider()
	}
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	res, err := telemetry.NewResource(ctx)
	if err != nil {
		log.Printf("Warning: metrics: failed to build the resource, falling back to Noop: %v", err)
		return metrics.NewNoopProvider()
	}
	var (
		reader sdkmetric.Reader
		spaced *spacedExporter
	)
	// The otlp exporter is built here rather than by autoexport, so that Close
	// can space the final export. autoexport keeps its producer registry
	// unexported, so OTEL_METRICS_PRODUCERS does not apply to it.
	switch os.Getenv("OTEL_METRICS_EXPORTER") {
	case "", "otlp":
		reader, spaced, err = newOTLPReader(ctx)
	default:
		reader, err = autoexport.NewMetricReader(ctx)
	}
	if err != nil {
		log.Printf("Warning: metrics: failed to create the metric exporter, falling back to Noop: %v", err)
		return metrics.NewNoopProvider()
	}
	return metrics.WrapBackend(newBackend(sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	), spaced))
}

// backend implements metrics.Backend on an OpenTelemetry MeterProvider.
type backend struct {
	provider *sdkmetric.MeterProvider
	meter    otelmetric.Meter
	spaced   *spacedExporter
}

var _ metrics.Backend = (*backend)(nil)

func newBackend(provider *sdkmetric.MeterProvider, spaced *spacedExporter) *backend {
	return &backend{
		provider: provider,
		meter:    provider.Meter(meterName),
		spaced:   spaced,
	}
}

// labelSets turns positional label values into cached attribute sets.
type labelSets struct {
	labels []string
	sets   sync.Map
}

func (l *labelSets) get(labelValues []string) attribute.Set {
	key := strings.Join(labelValues, "\x00")
	if set, ok := l.sets.Load(key); ok {
		return set.(attribute.Set)
	}
	kvs := make([]attribute.KeyValue, len(l.labels))
	for i, label := range l.labels {
		var v string
		if i < len(labelValues) {
			v = labelValues[i]
		}
		kvs[i] = attribute.String(label, v)
	}
	set := attribute.NewSet(kvs...)
	l.sets.Store(key, set)
	return set
}

type counter struct {
	labelSets
	inst otelmetric.Float64Counter
}

func (c *counter) Inc(labelValues ...string) {
	c.Add(1, labelValues...)
}

func (c *counter) Add(val float64, labelValues ...string) {
	c.inst.Add(context.Background(), val, otelmetric.WithAttributeSet(c.get(labelValues)))
}

type histogram struct {
	labelSets
	inst otelmetric.Float64Histogram
}

func (h *histogram) Observe(val float64, labelValues ...string) {
	h.inst.Record(context.Background(), val, otelmetric.WithAttributeSet(h.get(labelValues)))
}

// gauge keeps the current value per label set, since shared/metrics gauges
// support both Set and Add, and reports it when the reader collects.
type gauge struct {
	labelSets
	mu     sync.Mutex
	values map[attribute.Distinct]*gaugeValue
}

type gaugeValue struct {
	set   attribute.Set
	value float64
}

func (g *gauge) update(labelValues []string, f func(float64) float64) {
	set := g.get(labelValues)
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.values[set.Equivalent()]
	if !ok {
		v = &gaugeValue{set: set}
		g.values[set.Equivalent()] = v
	}
	v.value = f(v.value)
}

func (g *gauge) Set(val float64, labelValues ...string) {
	g.update(labelValues, func(float64) float64 { return val })
}

func (g *gauge) Add(val float64, labelValues ...string) {
	g.update(labelValues, func(old float64) float64 { return old + val })
}

func (g *gauge) observe(ctx context.Context, o otelmetric.Float64Observer) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, v := range g.values {
		o.Observe(v.value, otelmetric.WithAttributeSet(v.set))
	}
	return nil
}

// NewCounter implements metrics.Backend.
func (b *backend) NewCounter(name, help string, labels []string) metrics.Counter {
	inst, err := b.meter.Float64Counter(name, otelmetric.WithDescription(help))
	if err != nil {
		log.Printf("Warning: metrics: failed to create counter %q: %v", name, err)
	}
	return &counter{labelSets: labelSets{labels: labels}, inst: inst}
}

// NewGauge implements metrics.Backend.
func (b *backend) NewGauge(name, help string, labels []string) metrics.Gauge {
	g := &gauge{labelSets: labelSets{labels: labels}, values: make(map[attribute.Distinct]*gaugeValue)}
	if _, err := b.meter.Float64ObservableGauge(name, otelmetric.WithDescription(help), otelmetric.WithFloat64Callback(g.observe)); err != nil {
		log.Printf("Warning: metrics: failed to create gauge %q: %v", name, err)
	}
	return g
}

// NewHistogram implements metrics.Backend.
func (b *backend) NewHistogram(name, help string, buckets []float64, labels []string) metrics.Histogram {
	opts := []otelmetric.Float64HistogramOption{otelmetric.WithDescription(help)}
	if len(buckets) > 0 {
		opts = append(opts, otelmetric.WithExplicitBucketBoundaries(buckets...))
	}
	inst, err := b.meter.Float64Histogram(name, opts...)
	if err != nil {
		log.Printf("Warning: metrics: failed to create histogram %q: %v", name, err)
	}
	return &histogram{labelSets: labelSets{labels: labels}, inst: inst}
}

// Push implements metrics.Backend. The otlp reader exports on its own interval, and an
// extra export could come less than 5 s after a periodic one, so Push does nothing for it.
func (b *backend) Push(ctx context.Context) error {
	if b.spaced != nil {
		return nil
	}
	if err := b.provider.ForceFlush(ctx); err != nil {
		return fmt.Errorf("failed to flush metrics: %w", err)
	}
	return nil
}

// Close implements metrics.Backend.
func (b *backend) Close(ctx context.Context) error {
	if b.spaced != nil {
		ctx = b.spaced.waitForFinalExport(ctx)
	}
	if err := b.provider.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shut down metrics: %w", err)
	}
	return nil
}
