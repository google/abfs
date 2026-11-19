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

package servers

import (
	"context"
	"errors"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/metrics"
	"github.com/google/abfs/shared/remote"
)

const metricPrefix = "abfs_server"

var batchBuckets = []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000}

// MetricsServer records RPC counts by result and sizes of a RemoteCloser. RPC
// latency is recorded by the gRPC stats handler of the server.
type MetricsServer struct {
	server       remote.RemoteCloser
	requests     metrics.Counter
	inFlight     metrics.Gauge
	headersKeys  metrics.Counter
	headersBatch metrics.Histogram
	objectBytes  metrics.Counter
}

var _ remote.RemoteCloser = (*MetricsServer)(nil)

func NewMetricsServer(server remote.RemoteCloser, provider metrics.Provider) *MetricsServer {
	return &MetricsServer{
		server:       server,
		requests:     provider.NewCounter(metricPrefix+"_requests_total", "Count of RPCs handled by the server", []string{"method", "result"}),
		inFlight:     provider.NewGauge(metricPrefix+"_requests_in_flight", "Count of RPCs in progress", []string{"method"}),
		headersKeys:  provider.NewCounter(metricPrefix+"_headers_keys_total", "Count of keys looked up by Headers", []string{"result"}),
		headersBatch: provider.NewHistogram(metricPrefix+"_headers_batch_keys", "Count of keys per Headers call", batchBuckets, nil),
		objectBytes:  provider.NewCounter(metricPrefix+"_object_bytes_total", "Encoded object bytes read by PutObject and sent by GetObject", []string{"direction"}),
	}
}

func (s *MetricsServer) begin(method string) {
	s.inFlight.Add(1, method)
}

func (s *MetricsServer) end(method string, err error) {
	s.requests.Inc(method, result(err))
	s.inFlight.Add(-1, method)
}

func result(err error) string {
	var linkErr *remote.ErrObjectLinkValidation
	var permErr *remote.ErrPermissionDenied
	switch {
	case err == nil:
		return "ok"
	case entities.IsNotExist(err):
		return "not_found"
	case errors.As(err, &linkErr):
		return "missing_links"
	case errors.As(err, &permErr):
		return "permission_denied"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	default:
		return "error"
	}
}

// meteredReader counts the bytes read through it and reports them on Close.
type meteredReader struct {
	remote.ContentReadCloser
	n       int64
	closed  bool
	onClose func(n int64, err error)
}

func (r *meteredReader) Read(p []byte) (int, error) {
	n, err := r.ContentReadCloser.Read(p)
	r.n += int64(n)
	return n, err
}

func (r *meteredReader) Close() error {
	err := r.ContentReadCloser.Close()
	if !r.closed && r.onClose != nil {
		r.closed = true
		r.onClose(r.n, err)
	}
	return err
}

// Headers implements remote.RemoteProvider.
func (s *MetricsServer) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	s.begin("Headers")
	headers, err := s.server.Headers(ctx, keys)
	s.end("Headers", err)

	s.headersBatch.Observe(float64(len(keys)))
	if err == nil {
		notFound := 0
		for _, header := range headers {
			if header.IsEmpty() {
				notFound++
			}
		}
		s.headersKeys.Add(float64(len(headers)-notFound), "found")
		s.headersKeys.Add(float64(notFound), "not_found")
	}
	return headers, err
}

// SHA256 implements remote.RemoteProvider.
func (s *MetricsServer) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	s.begin("SHA256")
	shas, err := s.server.SHA256(ctx, keys)
	s.end("SHA256", err)
	return shas, err
}

// GetObject implements remote.RemoteProvider.
func (s *MetricsServer) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	s.begin("GetObject")
	reader, err := s.server.GetObject(ctx, key)
	if err != nil {
		s.end("GetObject", err)
		return nil, err
	}
	return &meteredReader{
		ContentReadCloser: reader,
		onClose: func(n int64, err error) {
			s.objectBytes.Add(float64(n), "out")
			s.end("GetObject", err)
		},
	}, nil
}

// PutObject implements remote.RemoteProvider.
func (s *MetricsServer) PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
	s.begin("PutObject")
	metered := &meteredReader{ContentReadCloser: reader}
	err := s.server.PutObject(ctx, key, metered)
	s.objectBytes.Add(float64(metered.n), "in")
	s.end("PutObject", err)
	return err
}

// MapResults implements remote.RemoteProvider.
func (s *MetricsServer) MapResults(ctx context.Context, from remote.Hash) ([]remote.Hash, error) {
	s.begin("MapResults")
	results, err := s.server.MapResults(ctx, from)
	s.end("MapResults", err)
	return results, err
}

// AddMapResult implements remote.RemoteProvider.
func (s *MetricsServer) AddMapResult(ctx context.Context, from remote.Hash, to remote.Hash) error {
	s.begin("AddMapResult")
	err := s.server.AddMapResult(ctx, from, to)
	s.end("AddMapResult", err)
	return err
}

// GetRefs implements remote.RemoteProvider.
func (s *MetricsServer) GetRefs(ctx context.Context, server string, project string, refs []string) ([]remote.Hash, error) {
	s.begin("GetRefs")
	hashes, err := s.server.GetRefs(ctx, server, project, refs)
	s.end("GetRefs", err)
	return hashes, err
}

// SearchRefs implements remote.RemoteProvider.
func (s *MetricsServer) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]remote.ProjectRefHash, error) {
	s.begin("SearchRefs")
	refs, err := s.server.SearchRefs(ctx, server, project, refPrefix, limit)
	s.end("SearchRefs", err)
	return refs, err
}

// PutRefs implements remote.RemoteProvider.
func (s *MetricsServer) PutRefs(ctx context.Context, server string, project string, refs []remote.RefHash) error {
	s.begin("PutRefs")
	err := s.server.PutRefs(ctx, server, project, refs)
	s.end("PutRefs", err)
	return err
}

// Mark implements remote.RemoteProvider.
func (s *MetricsServer) Mark(ctx context.Context, hash remote.Hash, key string, value string) (int64, error) {
	s.begin("Mark")
	count, err := s.server.Mark(ctx, hash, key, value)
	s.end("Mark", err)
	return count, err
}

// Version implements remote.RemoteProvider.
func (s *MetricsServer) Version(ctx context.Context) (string, error) {
	s.begin("Version")
	version, err := s.server.Version(ctx)
	s.end("Version", err)
	return version, err
}

// WhoAmI implements remote.RemoteProvider.
func (s *MetricsServer) WhoAmI(ctx context.Context) (string, error) {
	s.begin("WhoAmI")
	user, err := s.server.WhoAmI(ctx)
	s.end("WhoAmI", err)
	return user, err
}

// Close implements remote.RemoteCloser.
func (s *MetricsServer) Close() error {
	return s.server.Close()
}
