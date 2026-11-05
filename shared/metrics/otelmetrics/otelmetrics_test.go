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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	"google.golang.org/grpc/stats"

	"github.com/google/abfs/shared/telemetry"
)

func newTestBackend(t *testing.T) (*backend, *sdkmetric.ManualReader) {
	reader := sdkmetric.NewManualReader()
	b := newBackend(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), nil)
	t.Cleanup(func() {
		require.NoError(t, b.Close(context.Background()), "error closing backend")
	})
	return b, reader
}

func collect(t *testing.T, reader *sdkmetric.ManualReader, name string) metricdata.Aggregation {
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm), "error collecting metrics")
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m.Data
			}
		}
	}
	t.Fatalf("metric %q not collected", name)
	return nil
}

func attrs(kvs ...string) attribute.Set {
	var list []attribute.KeyValue
	for i := 0; i < len(kvs); i += 2 {
		list = append(list, attribute.String(kvs[i], kvs[i+1]))
	}
	return attribute.NewSet(list...)
}

func key(kvs ...string) attribute.Distinct {
	set := attrs(kvs...)
	return set.Equivalent()
}

func TestBackend_Counter(t *testing.T) {
	require := require.New(t)
	b, reader := newTestBackend(t)

	c := b.NewCounter("requests_total", "Count of requests", []string{"method", "result"})
	c.Inc("Headers", "ok")
	c.Add(2, "Headers", "ok")
	c.Inc("Headers", "not_found")

	sum, ok := collect(t, reader, "requests_total").(metricdata.Sum[float64])
	require.True(ok, "counter is not a sum")
	require.True(sum.IsMonotonic)
	require.Equal(metricdata.CumulativeTemporality, sum.Temporality)
	got := map[attribute.Distinct]float64{}
	for _, dp := range sum.DataPoints {
		got[dp.Attributes.Equivalent()] = dp.Value
	}
	require.Equal(map[attribute.Distinct]float64{
		key("method", "Headers", "result", "ok"):        3,
		key("method", "Headers", "result", "not_found"): 1,
	}, got)
}

func TestBackend_Gauge(t *testing.T) {
	require := require.New(t)
	b, reader := newTestBackend(t)

	g := b.NewGauge("in_flight", "Count of requests in progress", []string{"method"})
	g.Set(5, "Headers")
	g.Add(-2, "Headers")
	g.Add(1, "GetObject")

	gauge, ok := collect(t, reader, "in_flight").(metricdata.Gauge[float64])
	require.True(ok, "gauge is not a gauge")
	got := map[attribute.Distinct]float64{}
	for _, dp := range gauge.DataPoints {
		got[dp.Attributes.Equivalent()] = dp.Value
	}
	require.Equal(map[attribute.Distinct]float64{
		key("method", "Headers"):   3,
		key("method", "GetObject"): 1,
	}, got)
}

func TestBackend_Histogram(t *testing.T) {
	require := require.New(t)
	b, reader := newTestBackend(t)

	h := b.NewHistogram("duration_seconds", "Duration in seconds", []float64{0.001, 0.01, 0.1}, []string{"method"})
	h.Observe(0.0005, "Headers")
	h.Observe(0.05, "Headers")

	hist, ok := collect(t, reader, "duration_seconds").(metricdata.Histogram[float64])
	require.True(ok, "histogram is not a histogram")
	require.Equal(metricdata.CumulativeTemporality, hist.Temporality)
	require.Len(hist.DataPoints, 1)
	dp := hist.DataPoints[0]
	require.Equal([]float64{0.001, 0.01, 0.1}, dp.Bounds)
	require.Equal([]uint64{1, 0, 1, 0}, dp.BucketCounts)
	require.Equal(uint64(2), dp.Count)
	require.Equal(attrs("method", "Headers"), dp.Attributes)
}

func TestBackend_MissingLabelValues(t *testing.T) {
	b, reader := newTestBackend(t)

	b.NewCounter("requests_total", "Count of requests", []string{"method", "result"}).Inc("Headers")

	sum := collect(t, reader, "requests_total").(metricdata.Sum[float64])
	require.Len(t, sum.DataPoints, 1)
	require.Equal(t, attrs("method", "Headers", "result", ""), sum.DataPoints[0].Attributes)
}

func TestNewProvider_SetsMeterProvider(t *testing.T) {
	require := require.New(t)
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Cleanup(func() { telemetry.SetMeterProvider(nil) })

	newProvider(false)
	require.Nil(telemetry.MeterProvider(), "a disabled provider sets no meter provider")

	p := newProvider(true)
	t.Cleanup(func() { require.NoError(p.Close(context.Background())) })
	require.NotNil(telemetry.MeterProvider())
}

func TestNewMeterProvider_RPCDurationBuckets(t *testing.T) {
	require := require.New(t)
	reader := sdkmetric.NewManualReader()
	telemetry.SetMeterProvider(newMeterProvider(resource.Empty(), []sdkmetric.Reader{reader}))
	t.Cleanup(func() { telemetry.SetMeterProvider(nil) })

	h := telemetry.ServerHandler()
	start := time.Now()
	ctx := h.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/abfs.ABFS/Headers"})
	h.HandleRPC(ctx, &stats.Begin{BeginTime: start})
	h.HandleRPC(ctx, &stats.End{BeginTime: start, EndTime: start.Add(200 * time.Microsecond)})

	points := collect(t, reader, "rpc.server.call.duration").(metricdata.Histogram[float64]).DataPoints
	require.Len(points, 1)
	require.Equal(rpcDurationBuckets, points[0].Bounds)
	require.Equal(uint64(1), points[0].BucketCounts[2], "a 200 us call lands in the bucket up to 250 us")
}
