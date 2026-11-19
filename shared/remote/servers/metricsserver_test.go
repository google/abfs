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

package servers_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/metrics"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

// recordingBackend keeps the last value of every counter and gauge and every
// histogram observation, keyed by metric name and label values.
type recordingBackend struct {
	mu           sync.Mutex
	values       map[string]float64
	observations map[string][]float64
}

func newRecordingBackend() *recordingBackend {
	return &recordingBackend{
		values:       make(map[string]float64),
		observations: make(map[string][]float64),
	}
}

func metricKey(name string, labelValues []string) string {
	return name + "{" + strings.Join(labelValues, ",") + "}"
}

func (b *recordingBackend) add(name string, val float64, labelValues []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values[metricKey(name, labelValues)] += val
}

func (b *recordingBackend) value(name string, labelValues ...string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.values[metricKey(name, labelValues)]
}

func (b *recordingBackend) observed(name string, labelValues ...string) []float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.observations[metricKey(name, labelValues)]
}

func (b *recordingBackend) NewCounter(name, help string, labels []string) metrics.Counter {
	return &recordingMetric{b: b, name: name}
}

func (b *recordingBackend) NewGauge(name, help string, labels []string) metrics.Gauge {
	return &recordingMetric{b: b, name: name}
}

func (b *recordingBackend) NewHistogram(name, help string, buckets []float64, labels []string) metrics.Histogram {
	return &recordingMetric{b: b, name: name}
}

func (b *recordingBackend) Push(ctx context.Context) error  { return nil }
func (b *recordingBackend) Close(ctx context.Context) error { return nil }

type recordingMetric struct {
	b    *recordingBackend
	name string
}

func (m *recordingMetric) Inc(labelValues ...string) {
	m.b.add(m.name, 1, labelValues)
}

func (m *recordingMetric) Add(val float64, labelValues ...string) {
	m.b.add(m.name, val, labelValues)
}

func (m *recordingMetric) Set(val float64, labelValues ...string) {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	m.b.values[metricKey(m.name, labelValues)] = val
}

func (m *recordingMetric) Observe(val float64, labelValues ...string) {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	key := metricKey(m.name, labelValues)
	m.b.observations[key] = append(m.b.observations[key], val)
}

func newMeteredServer() (*servers.MetricsServer, *recordingBackend) {
	b := newRecordingBackend()
	return servers.NewMetricsServer(inmemory.New(), metrics.WrapBackend(b)), b
}

func TestMetricsServer_Headers(t *testing.T) {
	require := require.New(t)
	s, b := newMeteredServer()

	_, missing := testBlob("missing")
	headers, err := s.Headers(context.Background(), []remote.Hash{entities.EmptyFileHash, missing})
	require.NoError(err, "error reading Headers")
	require.True(headers[0].IsBlob())
	require.True(headers[1].IsEmpty())

	require.Equal(1.0, b.value("abfs_server_requests_total", "Headers", "ok"))
	require.Equal(0.0, b.value("abfs_server_requests_in_flight", "Headers"))
	require.Equal(1.0, b.value("abfs_server_headers_keys_total", "found"))
	require.Equal(1.0, b.value("abfs_server_headers_keys_total", "not_found"))
	require.Equal([]float64{2}, b.observed("abfs_server_headers_batch_keys"))
}

func TestMetricsServer_GetObject(t *testing.T) {
	require := require.New(t)
	s, b := newMeteredServer()

	content, key := testBlob("some content")
	encoded := remote.NewZStdContentReader(content)
	encodedBytes, err := io.ReadAll(encoded)
	require.NoError(err, "error encoding content")
	require.NoError(s.PutObject(context.Background(), key, remote.NewZStdContentReader(content)), "error from PutObject")
	require.Equal(float64(len(encodedBytes)), b.value("abfs_server_object_bytes_total", "in"))

	reader, err := s.GetObject(context.Background(), key)
	require.NoError(err, "error from GetObject")
	require.Equal(1.0, b.value("abfs_server_requests_in_flight", "GetObject"))
	require.Equal(0.0, b.value("abfs_server_requests_total", "GetObject", "ok"))

	got, err := io.ReadAll(reader)
	require.NoError(err, "error reading object")
	require.NoError(reader.Close(), "error closing object")
	require.NoError(reader.Close(), "error closing object twice")
	require.Equal(encodedBytes, got)
	require.Equal(remote.EncodingZStd, reader.Encoding())

	require.Equal(1.0, b.value("abfs_server_requests_total", "GetObject", "ok"))
	require.Equal(0.0, b.value("abfs_server_requests_in_flight", "GetObject"))
	require.Equal(float64(len(encodedBytes)), b.value("abfs_server_object_bytes_total", "out"))
}

func TestMetricsServer_Results(t *testing.T) {
	_, missing := testBlob("missing")
	tree := testTree(missing)

	tests := []struct {
		name   string
		method string
		call   func(s *servers.MetricsServer) error
		want   string
	}{
		{
			name:   "NotFound",
			method: "GetObject",
			call: func(s *servers.MetricsServer) error {
				_, err := s.GetObject(context.Background(), missing)
				return err
			},
			want: "not_found",
		},
		{
			name:   "MissingLinks",
			method: "PutObject",
			call: func(s *servers.MetricsServer) error {
				return s.PutObject(context.Background(), objects.HashBytes(tree), remote.NewZStdContentReader(tree))
			},
			want: "missing_links",
		},
		{
			name:   "Error",
			method: "WhoAmI",
			call: func(s *servers.MetricsServer) error {
				_, err := s.WhoAmI(context.Background())
				return err
			},
			want: "error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, b := newMeteredServer()
			require.Error(t, tt.call(s))
			require.Equal(t, 1.0, b.value("abfs_server_requests_total", tt.method, tt.want))
			require.Equal(t, 0.0, b.value("abfs_server_requests_in_flight", tt.method))
		})
	}
}

// cancelingServer returns the context error from Headers, wrapped as a backend would.
type cancelingServer struct {
	remote.RemoteCloser
}

func (s cancelingServer) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	if _, err := s.RemoteCloser.Headers(ctx, keys); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("failed to read headers: %w", ctx.Err())
}

func TestMetricsServer_Canceled(t *testing.T) {
	b := newRecordingBackend()
	s := servers.NewMetricsServer(cancelingServer{inmemory.New()}, metrics.WrapBackend(b))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Headers(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1.0, b.value("abfs_server_requests_total", "Headers", "canceled"))
}

func testBlob(content string) ([]byte, remote.Hash) {
	b := fmt.Appendf(nil, "blob %d\x00%s", len(content), content)
	return b, objects.HashBytes(b)
}

func testTree(file remote.Hash) []byte {
	body := append([]byte("100644 file.txt\x00"), file[:]...)
	return append(fmt.Appendf(nil, "tree %d\x00", len(body)), body...)
}
