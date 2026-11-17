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

package server

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/google/abfs/shared/metrics"
)

const proxyService = "test.Proxy"

// recordingBackend is a metrics backend that keeps the last value of every
// gauge, by name and label values.
type recordingBackend struct {
	mu     sync.Mutex
	values map[string]float64
}

func (b *recordingBackend) value(t *testing.T, name string, labelValues ...string) float64 {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.values[name+"{"+strings.Join(labelValues, ",")+"}"]
	require.True(t, ok, "gauge %s %v not set", name, labelValues)
	return v
}

func (b *recordingBackend) NewCounter(name, help string, labels []string) metrics.Counter {
	return nopMetric{}
}

func (b *recordingBackend) NewGauge(name, help string, labels []string) metrics.Gauge {
	return &recordingGauge{backend: b, name: name}
}

func (b *recordingBackend) NewHistogram(name, help string, buckets []float64, labels []string) metrics.Histogram {
	return nopMetric{}
}

func (b *recordingBackend) Push(ctx context.Context) error  { return nil }
func (b *recordingBackend) Close(ctx context.Context) error { return nil }

type recordingGauge struct {
	backend *recordingBackend
	name    string
}

func (g *recordingGauge) Set(val float64, labelValues ...string) {
	g.backend.mu.Lock()
	defer g.backend.mu.Unlock()
	g.backend.values[g.name+"{"+strings.Join(labelValues, ",")+"}"] = val
}

func (g *recordingGauge) Add(val float64, labelValues ...string) {
	g.backend.mu.Lock()
	defer g.backend.mu.Unlock()
	g.backend.values[g.name+"{"+strings.Join(labelValues, ",")+"}"] += val
}

type nopMetric struct{}

func (nopMetric) Inc(labelValues ...string)                  {}
func (nopMetric) Add(val float64, labelValues ...string)     {}
func (nopMetric) Observe(val float64, labelValues ...string) {}

// probeRun is a RunHealthProbe started on a health service where the ABFS
// service and a proxy prefix report SERVING, as NewGRPCServer leaves them.
type probeRun struct {
	hs      *health.Server
	tick    chan time.Time
	events  chan probeEvent
	metrics *recordingBackend
	cancel  context.CancelFunc
	stopped chan struct{}
}

func startHealthProbe(t *testing.T, probes map[string]func(context.Context) error, timeout time.Duration, failures int) *probeRun {
	t.Helper()
	hs := health.NewServer()
	hs.SetServingStatus("abfs.ABFS", grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(proxyService, grpc_health_v1.HealthCheckResponse_SERVING)
	r := &probeRun{
		hs:      hs,
		tick:    make(chan time.Time),
		events:  make(chan probeEvent, 100),
		metrics: &recordingBackend{values: make(map[string]float64)},
		stopped: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	opts := &HealthProbeOpts{
		Health:   hs,
		Probes:   probes,
		Interval: time.Hour,
		Timeout:  timeout,
		Failures: failures,
		Metrics:  metrics.WrapBackend(r.metrics),
		tick:     r.tick,
		recorded: func(e probeEvent) { r.events <- e },
	}
	go func() {
		defer close(r.stopped)
		if err := RunHealthProbe(ctx, opts); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-r.stopped
	})
	return r
}

// next waits for the next recorded event.
func (r *probeRun) next(t *testing.T) probeEvent {
	t.Helper()
	select {
	case e := <-r.events:
		return e
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a health probe result")
		return 0
	}
}

// round starts the next round with a tick and waits for its event.
func (r *probeRun) round(t *testing.T) probeEvent {
	t.Helper()
	r.tick <- time.Now()
	return r.next(t)
}

func (r *probeRun) status(t *testing.T, service string) grpc_health_v1.HealthCheckResponse_ServingStatus {
	t.Helper()
	resp, err := r.hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{Service: service})
	require.NoError(t, err)
	return resp.Status
}

func TestRunHealthProbe_Rules(t *testing.T) {
	const (
		S = grpc_health_v1.HealthCheckResponse_SERVING
		N = grpc_health_v1.HealthCheckResponse_NOT_SERVING
	)
	tests := []struct {
		name     string
		outcomes string // P passes, F fails, one round each
		want     string // status after each round: S or N
	}{
		{name: "two failures in a row stay serving", outcomes: "PPFFPP", want: "SSSSSS"},
		{name: "three failures in a row", outcomes: "FFF", want: "SSN"},
		{name: "three of the last five", outcomes: "FFPF", want: "SSSN"},
		{name: "one in three stays serving", outcomes: "FPPFPPF", want: "SSSSSSS"},
		{name: "window keeps its size", outcomes: "FPFPPPFPF", want: "SSSSSSSSS"},
		{name: "recovery needs three passes in a row", outcomes: "FFFPPFPP", want: "SSNNNNNN"},
		{name: "three passes in a row restore serving", outcomes: "FFFPPP", want: "SSNNNS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			results := make(chan error, len(tt.outcomes))
			for _, o := range tt.outcomes {
				if o == 'F' {
					results <- errors.New("backend down")
				} else {
					results <- nil
				}
			}
			r := startHealthProbe(t, map[string]func(context.Context) error{
				"store": func(context.Context) error { return <-results },
			}, time.Minute, 3)

			for i, o := range tt.outcomes {
				var e probeEvent
				if i == 0 {
					e = r.next(t)
				} else {
					e = r.round(t)
				}
				if o == 'F' {
					require.Equal(probeFailed, e, "round %d", i+1)
				} else {
					require.Equal(probePassed, e, "round %d", i+1)
				}
				want := S
				if tt.want[i] == 'N' {
					want = N
				}
				require.Equal(want, r.status(t, ""), "overall status after round %d", i+1)
				require.Equal(want, r.status(t, "abfs.ABFS"), "ABFS status after round %d", i+1)
				require.Equal(S, r.status(t, proxyService), "proxy status after round %d", i+1)
				wantGauge := 1.0
				if want == N {
					wantGauge = 0
				}
				require.Equal(wantGauge, r.metrics.value(t, "abfs_server_serving"), "after round %d", i+1)
			}
		})
	}
}

func TestRunHealthProbe_StuckProbe(t *testing.T) {
	require := require.New(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	r := startHealthProbe(t, map[string]func(context.Context) error{
		// Ignores its ctx, as a hung disk does.
		"store": func(context.Context) error {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
			return nil
		},
	}, time.Minute, 3)
	<-started

	for i := range 3 {
		require.Equal(probeStillRunning, r.round(t))
		want := grpc_health_v1.HealthCheckResponse_SERVING
		if i == 2 {
			want = grpc_health_v1.HealthCheckResponse_NOT_SERVING
		}
		require.Equal(want, r.status(t, ""), "after tick %d", i+1)
		require.Equal(0.0, r.metrics.value(t, "abfs_server_backend_up", "store"))
	}
	require.Equal(int32(1), calls.Load())

	// The late pass is discarded.
	close(release)
	require.Equal(probeDiscarded, r.next(t))
	require.Equal(grpc_health_v1.HealthCheckResponse_NOT_SERVING, r.status(t, ""))
	require.Equal(0.0, r.metrics.value(t, "abfs_server_backend_up", "store"))
	require.Equal(0.0, r.metrics.value(t, "abfs_server_serving"))

	for i := range 3 {
		require.Equal(probePassed, r.round(t))
		want := grpc_health_v1.HealthCheckResponse_NOT_SERVING
		if i == 2 {
			want = grpc_health_v1.HealthCheckResponse_SERVING
		}
		require.Equal(want, r.status(t, ""), "after pass %d", i+1)
	}
	require.Equal(int32(4), calls.Load())
	require.Equal(1.0, r.metrics.value(t, "abfs_server_backend_up", "store"))
}

func TestRunHealthProbe_LatePassFails(t *testing.T) {
	r := startHealthProbe(t, map[string]func(context.Context) error{
		"store": func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		},
	}, 10*time.Millisecond, 1)

	require.Equal(t, probeFailed, r.next(t))
	require.Equal(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING, r.status(t, ""))
	require.Equal(t, 0.0, r.metrics.value(t, "abfs_server_backend_up", "store"))
}

func TestRunHealthProbe_PerBackend(t *testing.T) {
	require := require.New(t)
	ok := func(context.Context) error { return nil }
	r := startHealthProbe(t, map[string]func(context.Context) error{
		"store":   ok,
		"index":   ok,
		"storage": func(context.Context) error { return errors.New("permission denied") },
	}, time.Minute, 3)

	require.Equal(probeFailed, r.next(t))
	require.Equal(probeFailed, r.round(t))
	require.Equal(grpc_health_v1.HealthCheckResponse_SERVING, r.status(t, ""))
	require.Equal(1.0, r.metrics.value(t, "abfs_server_serving"))
	require.Equal(probeFailed, r.round(t))
	require.Equal(grpc_health_v1.HealthCheckResponse_NOT_SERVING, r.status(t, ""))
	require.Equal(0.0, r.metrics.value(t, "abfs_server_serving"))
	require.Equal(0.0, r.metrics.value(t, "abfs_server_backend_up", "storage"))
	require.Equal(1.0, r.metrics.value(t, "abfs_server_backend_up", "store"))
	require.Equal(1.0, r.metrics.value(t, "abfs_server_backend_up", "index"))
}

func TestRunHealthProbe_StopsOnCancel(t *testing.T) {
	r := startHealthProbe(t, map[string]func(context.Context) error{
		"store": func(context.Context) error { return nil },
	}, time.Minute, 3)
	require.Equal(t, probePassed, r.next(t))
	require.Equal(t, 1.0, r.metrics.value(t, "abfs_server_serving"))

	r.cancel()
	select {
	case <-r.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("RunHealthProbe did not return after cancel")
	}
	require.Equal(t, 0.0, r.metrics.value(t, "abfs_server_serving"))
}

func TestRunHealthProbe_NoResumeAfterShutdown(t *testing.T) {
	require := require.New(t)
	var fail atomic.Bool
	fail.Store(true)
	r := startHealthProbe(t, map[string]func(context.Context) error{
		"store": func(context.Context) error {
			if fail.Load() {
				return errors.New("backend down")
			}
			return nil
		},
	}, time.Minute, 3)
	require.Equal(probeFailed, r.next(t))
	require.Equal(probeFailed, r.round(t))
	require.Equal(probeFailed, r.round(t))
	require.Equal(grpc_health_v1.HealthCheckResponse_NOT_SERVING, r.status(t, ""))

	// The drain shuts the health service down; passing rounds must not
	// report SERVING again.
	r.hs.Shutdown()
	fail.Store(false)
	for range 3 {
		require.Equal(probePassed, r.round(t))
	}
	require.Equal(grpc_health_v1.HealthCheckResponse_NOT_SERVING, r.status(t, ""))
	require.Equal(grpc_health_v1.HealthCheckResponse_NOT_SERVING, r.status(t, "abfs.ABFS"))
}

func TestRunHealthProbe_NoProbes(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	hs := health.NewServer()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := RunHealthProbe(ctx, &HealthProbeOpts{Health: hs, Interval: time.Second, Timeout: time.Second, Failures: 3}); err != nil {
			t.Error(err)
		}
	}()
	cancel()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("RunHealthProbe did not return after cancel")
	}

	require.Equal(t, 1, strings.Count(buf.String(), "\n"))
	require.Contains(t, buf.String(), "no backend health probe")
	resp, err := hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, resp.Status)
}

func TestRunHealthProbe_ReturnedRoundBeforeTick(t *testing.T) {
	require := require.New(t)
	type call struct {
		ctx     context.Context
		release chan struct{}
	}
	calls := make(chan call)
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time, 1)
	events := make(chan probeEvent, 100)
	// hold keeps the watcher in its callback after a tick counted a round as
	// still running, so that the round can return before the next tick.
	hold := make(chan struct{})
	opts := &HealthProbeOpts{
		Health: health.NewServer(),
		Probes: map[string]func(context.Context) error{
			"store": func(ctx context.Context) error {
				c := call{ctx: ctx, release: make(chan struct{})}
				select {
				case calls <- c:
				case <-ctx.Done():
					return ctx.Err()
				}
				<-c.release
				return nil
			},
		},
		Interval: time.Hour,
		Timeout:  time.Minute,
		Failures: 3,
		Metrics:  metrics.WrapBackend(&recordingBackend{values: make(map[string]float64)}),
		tick:     tick,
		recorded: func(e probeEvent) {
			events <- e
			if e == probeStillRunning {
				select {
				case <-hold:
				case <-ctx.Done():
				}
			}
		},
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := RunHealthProbe(ctx, opts); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	next := func() probeEvent {
		select {
		case e := <-events:
			return e
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for a health probe result")
			return 0
		}
	}

	// The watcher picks at random between a ready tick and a ready result, so
	// repeat until a watcher that judges the tick first would have been caught.
	for i := range 20 {
		c := <-calls
		tick <- time.Now()
		require.Equal(probeStillRunning, next(), "cycle %d", i+1)
		close(c.release)
		// The round's ctx is cancelled after runProbeRound sent the round to
		// the watcher, so now both the result and the next tick are ready.
		<-c.ctx.Done()
		tick <- time.Now()
		hold <- struct{}{}
		require.Equal(probeDiscarded, next(), "cycle %d: a returned round was counted as still running", i+1)
	}
}

func TestRunHealthProbe_InvalidOptions(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		timeout  time.Duration
		failures int
	}{
		{name: "zero interval", interval: 0, timeout: time.Second, failures: 3},
		{name: "zero timeout", interval: time.Second, timeout: 0, failures: 3},
		{name: "zero failures", interval: time.Second, timeout: time.Second, failures: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hs := health.NewServer()
			err := RunHealthProbe(context.Background(), &HealthProbeOpts{
				Health:   hs,
				Probes:   map[string]func(context.Context) error{"store": func(context.Context) error { return nil }},
				Interval: tt.interval,
				Timeout:  tt.timeout,
				Failures: tt.failures,
			})
			require.ErrorContains(t, err, "invalid health probe")
			resp, err := hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
			require.NoError(t, err)
			require.Equal(t, grpc_health_v1.HealthCheckResponse_SERVING, resp.Status)
		})
	}
}
