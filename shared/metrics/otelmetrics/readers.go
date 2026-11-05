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
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// readers holds the metric readers named by OTEL_METRICS_EXPORTER.
type readers struct {
	readers []sdkmetric.Reader
	spaced  *spacedExporter
	servers []*prometheusServer
}

// newReaders builds a reader for each exporter in the comma-separated list
// names. A single name other than otlp is built by autoexport, as before. In a
// list, otlp, prometheus, console and none are built here, and listeners are
// not started until start is called.
func newReaders(ctx context.Context, names string) (*readers, error) {
	r := &readers{}
	list := strings.Split(names, ",")
	if len(list) == 1 {
		// The otlp exporter is built here rather than by autoexport, so that
		// Close can space the final export. autoexport keeps its producer
		// registry unexported, so OTEL_METRICS_PRODUCERS does not apply to it.
		var (
			reader sdkmetric.Reader
			err    error
		)
		switch strings.TrimSpace(names) {
		case "", "otlp":
			reader, r.spaced, err = newOTLPReader(ctx)
		default:
			reader, err = autoexport.NewMetricReader(ctx)
		}
		if err != nil {
			return nil, err
		}
		r.readers = append(r.readers, reader)
		return r, nil
	}

	seen := make(map[string]bool)
	for _, name := range list {
		name = strings.TrimSpace(name)
		if seen[name] {
			r.shutdown(ctx)
			return nil, fmt.Errorf("exporter %q is listed twice in %q", name, names)
		}
		seen[name] = true
		var (
			reader sdkmetric.Reader
			err    error
		)
		switch name {
		case "otlp":
			reader, r.spaced, err = newOTLPReader(ctx)
		case "prometheus":
			var server *prometheusServer
			reader, server, err = newPrometheusReader()
			if err == nil {
				r.servers = append(r.servers, server)
			}
		case "console":
			var exporter sdkmetric.Exporter
			exporter, err = stdoutmetric.New()
			if err == nil {
				reader = sdkmetric.NewPeriodicReader(exporter)
			}
		case "none":
			continue
		default:
			err = fmt.Errorf("unsupported exporter %q in %q, a list takes otlp, prometheus, console and none", name, names)
		}
		if err != nil {
			r.shutdown(ctx)
			return nil, err
		}
		r.readers = append(r.readers, reader)
	}
	return r, nil
}

// start starts the listeners of the prometheus readers.
func (r *readers) start() error {
	for _, s := range r.servers {
		if err := s.start(); err != nil {
			return err
		}
	}
	return nil
}

// shutdown shuts down readers that are not registered with a provider yet.
func (r *readers) shutdown(ctx context.Context) {
	for _, reader := range r.readers {
		if err := reader.Shutdown(ctx); err != nil {
			log.Printf("Warning: metrics: failed to shut down a metric reader: %v", err)
		}
	}
}

// prometheusServer serves the registry of a prometheus reader once started.
type prometheusServer struct {
	addr   string
	server *http.Server
}

func (s *prometheusServer) start() error {
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s for the Prometheus exporter: %w", s.addr, err)
	}
	go func() {
		if err := s.server.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Warning: metrics: the Prometheus server stopped: %v", err)
		}
	}()
	return nil
}

// prometheusReader stops the server of its reader on Shutdown.
type prometheusReader struct {
	sdkmetric.Reader
	server *http.Server
}

func (r prometheusReader) Shutdown(ctx context.Context) error {
	return errors.Join(r.Reader.Shutdown(ctx), r.server.Shutdown(ctx))
}

// newPrometheusReader builds the reader autoexport builds for prometheus: its
// own registry, served at /metrics on OTEL_EXPORTER_PROMETHEUS_HOST (default
// localhost) and OTEL_EXPORTER_PROMETHEUS_PORT (default 9464).
func newPrometheusReader() (sdkmetric.Reader, *prometheusServer, error) {
	reg := prometheus.NewRegistry()
	reader, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create the Prometheus exporter: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	server := &http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
		Handler:      mux,
	}
	host := getenv("OTEL_EXPORTER_PROMETHEUS_HOST", "localhost")
	port := getenv("OTEL_EXPORTER_PROMETHEUS_PORT", "9464")
	return prometheusReader{Reader: reader, server: server}, &prometheusServer{addr: net.JoinHostPort(host, port), server: server}, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
