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
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/telemetry"
)

// freePort returns a local TCP port that was free a moment ago.
func freePort(t *testing.T) string {
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err, "error finding a free port")
	port := lis.Addr().(*net.TCPAddr).Port
	require.NoError(t, lis.Close(), "error closing listener")
	return strconv.Itoa(port)
}

func listening(port string) bool {
	conn, err := net.Dial("tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func TestNewReaders_Single(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		names      string
		wantSpaced bool
	}{
		{names: "", wantSpaced: true},
		{names: "otlp", wantSpaced: true},
		{names: "none", wantSpaced: false},
		{names: "console", wantSpaced: false},
	} {
		t.Run(tt.names, func(t *testing.T) {
			r, err := newReaders(ctx, tt.names)
			require.NoError(t, err, "error building readers")
			t.Cleanup(func() { r.shutdown(ctx) })
			require.Len(t, r.readers, 1)
			require.Equal(t, tt.wantSpaced, r.spaced != nil)
		})
	}
}

func TestNewReaders_List(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	port := freePort(t)
	t.Setenv("OTEL_EXPORTER_PROMETHEUS_PORT", port)

	r, err := newReaders(ctx, "prometheus, otlp,none")
	require.NoError(err, "error building readers")
	t.Cleanup(func() { r.shutdown(ctx) })
	require.Len(r.readers, 2)
	require.NotNil(r.spaced, "otlp in a list keeps the spaced final export")
	require.False(listening(port), "no listener before start")

	require.NoError(r.start(), "error starting listeners")
	resp, err := http.Get("http://" + net.JoinHostPort("localhost", port) + "/metrics")
	require.NoError(err, "error scraping")
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(resp.Body.Close())
	require.Equal(http.StatusOK, resp.StatusCode)

	r.shutdown(ctx)
	require.False(listening(port), "shutdown stops the listener")
}

func TestNewReaders_ListErrors(t *testing.T) {
	ctx := context.Background()
	for _, names := range []string{
		"otlp,otlp",
		"prometheus,unknown",
		"otlp,",
	} {
		t.Run(names, func(t *testing.T) {
			port := freePort(t)
			t.Setenv("OTEL_EXPORTER_PROMETHEUS_PORT", port)
			_, err := newReaders(ctx, names)
			require.Error(t, err)
			require.False(t, listening(port), "a failed list leaves no listener")
		})
	}
}

func TestNewProvider_ListenerFailure(t *testing.T) {
	require := require.New(t)
	busy, err := net.Listen("tcp", "localhost:0")
	require.NoError(err, "error listening")
	t.Cleanup(func() { _ = busy.Close() })
	t.Setenv("OTEL_EXPORTER_PROMETHEUS_PORT", strconv.Itoa(busy.Addr().(*net.TCPAddr).Port))
	t.Setenv("OTEL_METRICS_EXPORTER", "otlp,prometheus")
	t.Cleanup(func() { telemetry.SetMeterProvider(nil) })

	p := newProvider(true)
	require.NoError(p.Close(context.Background()))
	require.Nil(telemetry.MeterProvider(), "a provider that failed to start is not recorded")
}

func TestNewProvider_ListenerFailureSlowEndpoint(t *testing.T) {
	require := require.New(t)
	// The OTLP endpoint accepts connections and never answers.
	silent, err := net.Listen("tcp", "localhost:0")
	require.NoError(err, "error listening")
	t.Cleanup(func() { _ = silent.Close() })
	go func() {
		var conns []net.Conn
		defer func() {
			for _, c := range conns {
				_ = c.Close()
			}
		}()
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c)
		}
	}()
	busy, err := net.Listen("tcp", "localhost:0")
	require.NoError(err, "error listening")
	t.Cleanup(func() { _ = busy.Close() })
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+silent.Addr().String())
	t.Setenv("OTEL_EXPORTER_PROMETHEUS_PORT", strconv.Itoa(busy.Addr().(*net.TCPAddr).Port))
	t.Setenv("OTEL_METRICS_EXPORTER", "otlp,prometheus")
	t.Cleanup(func() { telemetry.SetMeterProvider(nil) })

	start := time.Now()
	p := newProvider(true)
	require.Less(time.Since(start), 5*time.Second, "a failed start does not wait for the final export")
	require.NoError(p.Close(context.Background()))
	require.Nil(telemetry.MeterProvider())
}

func TestNewProvider_List(t *testing.T) {
	require := require.New(t)
	port := freePort(t)
	t.Setenv("OTEL_EXPORTER_PROMETHEUS_PORT", port)
	t.Setenv("OTEL_METRICS_EXPORTER", "prometheus,console")
	t.Cleanup(func() { telemetry.SetMeterProvider(nil) })

	p := newProvider(true)
	p.NewCounter("abfs_test_total", "Count of tests", nil).Inc()
	resp, err := http.Get("http://" + net.JoinHostPort("localhost", port) + "/metrics")
	require.NoError(err, "error scraping")
	body, err := io.ReadAll(resp.Body)
	require.NoError(err)
	require.NoError(resp.Body.Close())
	require.Contains(string(body), "abfs_test_total")
	require.NotNil(telemetry.MeterProvider())

	require.NoError(p.Close(context.Background()))
	require.False(listening(port), "Close stops the listener")
}
