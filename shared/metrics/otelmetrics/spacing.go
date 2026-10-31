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

package otelmetrics

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Some backends reject a point written less than 5 s after the previous point
// of the same series, also when it comes through a collector.
const minPointSpacing = 5 * time.Second

// newOTLPReader builds the reader autoexport builds for "otlp".
func newOTLPReader(ctx context.Context) (sdkmetric.Reader, *spacedExporter, error) {
	proto := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL")
	if proto == "" {
		proto = os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}
	var (
		exporter sdkmetric.Exporter
		err      error
	)
	switch proto {
	case "grpc":
		exporter, err = otlpmetricgrpc.New(ctx)
	case "", "http/protobuf":
		exporter, err = otlpmetrichttp.New(ctx)
	default:
		return nil, nil, fmt.Errorf("invalid OTLP protocol %q, should be one of grpc, http/protobuf", proto)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create the OTLP exporter: %w", err)
	}
	reader, spaced := newSpacedReader(exporter)
	return reader, spaced, nil
}

func newSpacedReader(exporter sdkmetric.Exporter) (*sdkmetric.PeriodicReader, *spacedExporter) {
	spaced := newSpacedExporter(exporter)
	return sdkmetric.NewPeriodicReader(spaced), spaced
}

type finalExportKey struct{}

// spacedExporter spaces the final export at shutdown from the last periodic one.
type spacedExporter struct {
	sdkmetric.Exporter
	spacing    time.Duration
	now        func() time.Time
	after      func(time.Duration) <-chan time.Time
	cumulative bool

	// sem guards the fields below and is held for a whole export, as a channel
	// so that Close's wait can end with ctx.
	sem     chan struct{}
	last    time.Time
	closing bool
	finalAt time.Time

	unknown atomic.Bool
	skipped atomic.Bool
}

var _ sdkmetric.Exporter = (*spacedExporter)(nil)

func newSpacedExporter(exporter sdkmetric.Exporter) *spacedExporter {
	return &spacedExporter{
		Exporter: exporter,
		spacing:  minPointSpacing,
		now:      time.Now,
		after:    time.After,
		cumulative: exporter.Temporality(sdkmetric.InstrumentKindCounter) == metricdata.CumulativeTemporality &&
			exporter.Temporality(sdkmetric.InstrumentKindHistogram) == metricdata.CumulativeTemporality,
		sem: make(chan struct{}, 1),
	}
}

func (e *spacedExporter) lock(ctx context.Context) bool {
	select {
	case e.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *spacedExporter) unlock() {
	<-e.sem
}

// waitForFinalExport waits until the final export is spaced from the last
// periodic export, or until the wait would pass the deadline of ctx, and
// returns the context to shut down with.
func (e *spacedExporter) waitForFinalExport(ctx context.Context) context.Context {
	final := context.WithValue(ctx, finalExportKey{}, true)
	for rechecked := false; ; rechecked = true {
		if !e.lock(ctx) {
			e.unknown.Store(true)
			return final
		}
		e.closing = true
		e.finalAt = e.now()
		noExport := e.last.IsZero()
		wait := e.last.Add(e.spacing).Sub(e.finalAt)
		e.unlock()
		if noExport || wait <= 0 {
			return final
		}
		// Only a delta tick moves the reference during the wait; chasing ticks
		// at a 5 s interval would last until the deadline.
		if rechecked {
			return final
		}
		if deadline, ok := ctx.Deadline(); ok && deadline.Sub(e.finalAt) < wait {
			return final
		}
		select {
		case <-e.after(wait):
		case <-ctx.Done():
			return final
		}
	}
}

// Export implements sdkmetric.Exporter.
func (e *spacedExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	final := ctx.Value(finalExportKey{}) != nil
	if !e.lock(ctx) {
		if final {
			e.skipped.Store(true)
			return nil
		}
		return ctx.Err()
	}
	defer e.unlock()
	if final {
		// All batches of the collection compare with the same reference.
		if e.unknown.Load() || (!e.last.IsZero() && e.last.Add(e.spacing).After(e.finalAt)) {
			e.skipped.Store(true)
			return nil
		}
		return e.Exporter.Export(ctx, rm)
	}
	if e.closing && e.cumulative {
		// With an interval of 5 s every tick would land just before the end of
		// the wait and start it again. The final export carries the same
		// cumulative values, so ticks during the wait are not sent.
		return nil
	}
	// The backend compares point times, which are set at collection, before
	// this call. Every attempt counts, since a failed or aborted request may
	// still have landed.
	e.last = e.now()
	return e.Exporter.Export(ctx, rm)
}

// Shutdown implements sdkmetric.Exporter.
func (e *spacedExporter) Shutdown(ctx context.Context) error {
	switch {
	case e.skipped.Load() && e.unknown.Load():
		log.Printf("Warning: metrics: an export in flight outlasted the shutdown deadline, skipped the final export")
	case e.skipped.Load():
		log.Printf("Warning: metrics: skipped the final export, it would have come less than %v after the previous one", e.spacing)
	}
	return e.Exporter.Shutdown(ctx)
}
