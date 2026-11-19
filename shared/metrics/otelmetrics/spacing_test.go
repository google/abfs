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
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fakeClock advances only when the code under test waits on it.
type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	waits []time.Duration
	// onWait, if set, runs once in place of advancing the clock.
	onWait func(d time.Duration)
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) after(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	onWait := c.onWait
	c.onWait = nil
	c.mu.Unlock()
	if onWait != nil {
		onWait(d)
	} else {
		c.advance(d)
	}
	ch := make(chan time.Time, 1)
	ch <- c.now()
	return ch
}

type exportRecord struct {
	phase  string
	at     time.Time
	points int
}

// fakeExporter records the time and size of every batch.
type fakeExporter struct {
	clock *fakeClock
	delta bool

	mu      sync.Mutex
	phase   string
	err     error
	exports []exportRecord
}

func (e *fakeExporter) set(phase string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.phase, e.err = phase, err
}

func (e *fakeExporter) batches(phase string) []exportRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	var got []exportRecord
	for _, r := range e.exports {
		if r.phase == phase {
			got = append(got, r)
		}
	}
	return got
}

func (e *fakeExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	if e.delta {
		return metricdata.DeltaTemporality
	}
	return sdkmetric.DefaultTemporalitySelector(k)
}

func (e *fakeExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (e *fakeExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	points := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			points += len(m.Data.(metricdata.Sum[float64]).DataPoints)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.exports = append(e.exports, exportRecord{phase: e.phase, at: e.clock.now(), points: points})
	return e.err
}

func (e *fakeExporter) ForceFlush(context.Context) error { return nil }

func (e *fakeExporter) Shutdown(context.Context) error { return nil }

// newSpacedTestBackend returns a backend whose collection of five points is
// exported in three batches, with no periodic tick during the test. Calling
// touch updates all five points.
func newSpacedTestBackend(clock *fakeClock, delta bool) (b *backend, exporter *fakeExporter, touch func()) {
	exporter = &fakeExporter{clock: clock, delta: delta}
	spaced := newSpacedExporter(exporter)
	spaced.now = clock.now
	spaced.after = clock.after
	reader := sdkmetric.NewPeriodicReader(spaced, sdkmetric.WithMaxExportBatchSize(2), sdkmetric.WithInterval(time.Hour))
	b = newBackend(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), spaced)
	c := b.NewCounter("requests_total", "Count of requests", []string{"method"})
	touch = func() {
		for _, method := range []string{"a", "b", "c", "d", "e"} {
			c.Inc(method)
		}
	}
	return b, exporter, touch
}

// captureLog returns the log output written during the test.
func captureLog(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func batchSizes(records []exportRecord) []int {
	var sizes []int
	for _, r := range records {
		sizes = append(sizes, r.points)
	}
	return sizes
}

func TestBackend_PushLeavesExportToReader(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	b, exporter, touch := newSpacedTestBackend(clock, false)
	touch()
	exporter.set("push", nil)
	require.NoError(t, b.Push(context.Background()), "error from Push")
	require.Empty(t, exporter.batches("push"), "Push must not export besides the periodic reader")
}

func TestBackend_CloseSpacesFinalExport(t *testing.T) {
	errUnavailable := errors.New("unavailable")
	tests := []struct {
		name string
		// periodic exports once before Close, failing with periodicErr.
		periodic    bool
		periodicErr error
		// tickAfter, if set, runs a periodic export that long into the first wait.
		tickAfter time.Duration
		delta     bool
		deadline  time.Duration
		wantWaits []time.Duration
		wantTick  []int
		wantFinal bool
	}{
		{
			name:      "NoExportYet",
			deadline:  15 * time.Second,
			wantFinal: true,
		},
		{
			name:      "AfterPeriodicExport",
			periodic:  true,
			deadline:  15 * time.Second,
			wantWaits: []time.Duration{4900 * time.Millisecond},
			wantFinal: true,
		},
		{
			name:        "AfterFailedPeriodicExport",
			periodic:    true,
			periodicErr: errUnavailable,
			deadline:    15 * time.Second,
			wantWaits:   []time.Duration{4900 * time.Millisecond},
			wantFinal:   true,
		},
		{
			name:      "TickDuringWait",
			periodic:  true,
			tickAfter: 4 * time.Second,
			deadline:  15 * time.Second,
			wantWaits: []time.Duration{4900 * time.Millisecond},
			wantFinal: true,
		},
		{
			name:     "DeadlineTooShort",
			periodic: true,
			deadline: 2 * time.Second,
		},
		{
			name:      "DeltaTickDuringWait",
			periodic:  true,
			tickAfter: 4 * time.Second,
			delta:     true,
			deadline:  15 * time.Second,
			wantWaits: []time.Duration{4900 * time.Millisecond},
			wantTick:  []int{2, 2, 1},
		},
		{
			name:      "DeltaTickDuringWaitDeadlineTooShort",
			periodic:  true,
			tickAfter: 4 * time.Second,
			delta:     true,
			deadline:  7 * time.Second,
			wantWaits: []time.Duration{4900 * time.Millisecond},
			wantTick:  []int{2, 2, 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{t: time.Now()}
			b, exporter, touch := newSpacedTestBackend(clock, tt.delta)

			if tt.periodic {
				touch()
				exporter.set("periodic", tt.periodicErr)
				err := b.provider.ForceFlush(context.Background())
				if tt.periodicErr != nil {
					require.ErrorIs(t, err, tt.periodicErr)
				} else {
					require.NoError(t, err, "error from ForceFlush")
				}
				require.Equal(t, []int{2, 2, 1}, batchSizes(exporter.batches("periodic")), "periodic batches")
				clock.advance(100 * time.Millisecond)
			}
			if tt.tickAfter > 0 {
				clock.onWait = func(d time.Duration) {
					clock.advance(tt.tickAfter)
					touch()
					exporter.set("tick", nil)
					require.NoError(t, b.provider.ForceFlush(context.Background()), "error from the tick")
					touch()
					exporter.set("final", nil)
					clock.advance(d - tt.tickAfter)
				}
			}

			touch()
			exporter.set("final", nil)
			ctx, cancel := context.WithDeadline(context.Background(), clock.now().Add(tt.deadline))
			defer cancel()
			logs := captureLog(t)
			err := b.Close(ctx)

			final := exporter.batches("final")
			require.NoError(t, err, "error from Close")
			if tt.wantFinal {
				require.Equal(t, []int{2, 2, 1}, batchSizes(final), "final batches")
				if last := append(exporter.batches("periodic"), exporter.batches("tick")...); len(last) > 0 {
					gap := final[0].at.Sub(last[len(last)-1].at)
					require.GreaterOrEqual(t, gap, minPointSpacing, "final export %v after the last periodic export", gap)
				}
			} else {
				require.Empty(t, final, "final batches sent too soon")
			}
			require.Equal(t, !tt.wantFinal, b.spaced.skipped.Load(), "final export skipped")
			require.Equal(t, !tt.wantFinal, strings.Contains(logs.String(), "it would have come less than 5s after the previous one"), "warning in %q", logs)
			require.Equal(t, tt.wantTick, batchSizes(exporter.batches("tick")), "tick batches")
			require.Equal(t, tt.wantWaits, clock.waits, "waits")
		})
	}
}

// blockingExporter holds its first export until release is closed or ctx ends.
type blockingExporter struct {
	*fakeExporter
	entered, release chan struct{}
	once             sync.Once
}

func newBlockingExporter(clock *fakeClock) *blockingExporter {
	return &blockingExporter{fakeExporter: &fakeExporter{clock: clock}, entered: make(chan struct{}), release: make(chan struct{})}
}

func (e *blockingExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	var err error
	e.once.Do(func() {
		close(e.entered)
		select {
		case <-e.release:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	if err != nil {
		return err
	}
	return e.fakeExporter.Export(ctx, rm)
}

func TestSpacedExporter_CloseWaitsForExportInFlight(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	exporter := newBlockingExporter(clock)
	spaced := newSpacedExporter(exporter)
	spaced.now = clock.now
	spaced.after = clock.after

	exporter.set("periodic", nil)
	done := make(chan error, 1)
	go func() { done <- spaced.Export(context.Background(), &metricdata.ResourceMetrics{}) }()
	<-exporter.entered
	time.AfterFunc(10*time.Millisecond, func() { close(exporter.release) })
	ctx, cancel := context.WithDeadline(context.Background(), clock.now().Add(15*time.Second))
	defer cancel()
	ctx = spaced.waitForFinalExport(ctx)

	require.Len(t, exporter.batches("periodic"), 1, "Close did not wait for the export in flight")
	require.NoError(t, <-done, "error from the periodic export")
	require.Equal(t, []time.Duration{minPointSpacing}, clock.waits, "waits")
	exporter.set("final", nil)
	require.NoError(t, spaced.Export(ctx, &metricdata.ResourceMetrics{}), "error from the final export")
	require.Len(t, exporter.batches("final"), 1, "final batches")
	require.False(t, spaced.skipped.Load(), "final export skipped")
}

func TestBackend_CloseExportInFlightOutlastsDeadline(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	exporter := newBlockingExporter(clock)
	spaced := newSpacedExporter(exporter)
	spaced.now = clock.now
	spaced.after = clock.after
	reader := sdkmetric.NewPeriodicReader(spaced, sdkmetric.WithInterval(time.Hour))
	b := newBackend(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), spaced)
	b.NewCounter("requests_total", "Count of requests", []string{"method"}).Inc("a")

	// The export never returns by itself, as one retrying for its whole timeout.
	exporter.set("periodic", nil)
	pushed := make(chan error, 1)
	go func() { pushed <- b.provider.ForceFlush(context.Background()) }()
	<-exporter.entered

	exporter.set("final", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	// The reader does not collect with an expired ctx, so Close reports it.
	require.ErrorIs(t, b.Close(ctx), context.DeadlineExceeded)

	require.Error(t, <-pushed, "the export in flight was not aborted")
	require.Empty(t, exporter.batches("final"), "final batches sent with unknown spacing")
	require.Empty(t, clock.waits, "waits")
}

func TestSpacedExporter_UnknownSpacing(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	exporter := newBlockingExporter(clock)
	spaced := newSpacedExporter(exporter)
	spaced.now = clock.now
	spaced.after = clock.after

	exporter.set("periodic", nil)
	done := make(chan error, 1)
	go func() { done <- spaced.Export(context.Background(), &metricdata.ResourceMetrics{}) }()
	<-exporter.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	spaced.waitForFinalExport(ctx)
	close(exporter.release)
	require.NoError(t, <-done, "error from the periodic export")

	// Even with time left, a final export after an unknown spacing is not sent.
	exporter.set("final", nil)
	final := context.WithValue(context.Background(), finalExportKey{}, true)
	require.NoError(t, spaced.Export(final, &metricdata.ResourceMetrics{}), "error from the final export")
	require.Empty(t, exporter.batches("final"), "final batch sent with unknown spacing")
	require.True(t, spaced.skipped.Load(), "final export not marked skipped")
	logs := captureLog(t)
	require.NoError(t, spaced.Shutdown(context.Background()), "error from Shutdown")
	require.Contains(t, logs.String(), "an export in flight outlasted the shutdown deadline, skipped the final export")
}

func TestNewOTLPReader(t *testing.T) {
	tests := []struct {
		name         string
		metricsProto string
		proto        string
		want         sdkmetric.Exporter
		wantErr      bool
	}{
		{name: "Default", want: &otlpmetrichttp.Exporter{}},
		{name: "GRPC", proto: "grpc", want: &otlpmetricgrpc.Exporter{}},
		{name: "HTTP", proto: "http/protobuf", want: &otlpmetrichttp.Exporter{}},
		{name: "MetricsProtocolWins", metricsProto: "grpc", proto: "http/protobuf", want: &otlpmetricgrpc.Exporter{}},
		{name: "Invalid", proto: "http/json", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", tt.metricsProto)
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", tt.proto)
			reader, spaced, err := newOTLPReader(context.Background())
			if tt.wantErr {
				require.ErrorContains(t, err, "invalid OTLP protocol")
				return
			}
			require.NoError(t, err, "error from newOTLPReader")
			require.IsType(t, tt.want, spaced.Exporter)
			require.NoError(t, reader.Shutdown(context.Background()), "error shutting down the reader")
		})
	}
}
