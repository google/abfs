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
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/google/abfs/shared/metrics"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
)

// HealthProbeOpts contains the options for RunHealthProbe.
type HealthProbeOpts struct {
	// Health is the health service whose status follows the probes.
	Health *health.Server
	// Services are the services to set. Nil means the overall status ("") and
	// the ABFS service.
	Services []string
	// Probes are the backend checks of one round, by backend name.
	Probes map[string]func(context.Context) error
	// Interval is the time between the starts of two rounds.
	Interval time.Duration
	// Timeout bounds one round. A check that returns after it fails the round.
	Timeout time.Duration
	// Failures is the number of failed rounds among the last 2*Failures-1 that
	// reports NOT_SERVING, and the number of passed rounds in a row that
	// reports SERVING again.
	Failures int
	// Metrics creates the health gauges. Nil means metrics.GetProvider().
	Metrics metrics.Provider

	// tick replaces the interval ticker in tests.
	tick <-chan time.Time
	// recorded is called in tests after each round outcome or discarded result
	// has been applied.
	recorded func(probeEvent)
}

type probeEvent int

const (
	probePassed probeEvent = iota
	probeFailed
	probeStillRunning
	probeDiscarded
)

// probeRound is one run of every check. Its fields other than id and start
// are written by the check goroutines.
type probeRound struct {
	id    int
	start time.Time
	mu    sync.Mutex
	errs  map[string]error
}

func (r *probeRound) set(name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs[name] = err
}

// results returns the outcome of every check that has returned, and the names
// of those still running.
func (r *probeRound) results(names []string) (map[string]error, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	done := make(map[string]error, len(r.errs))
	var running []string
	for _, name := range names {
		if err, ok := r.errs[name]; ok {
			done[name] = err
		} else {
			running = append(running, name)
		}
	}
	return done, running
}

// RunHealthProbe runs the backend checks every interval and sets the serving
// status of the services in the health service from their results, until ctx
// is done. It never resumes a health service that was shut down. Interval and
// Timeout must be above 0 and Failures 1 or more; otherwise it returns an error
// at once. It returns nil when ctx is done.
func RunHealthProbe(ctx context.Context, o *HealthProbeOpts) error {
	if o.Interval <= 0 || o.Timeout <= 0 {
		return fmt.Errorf("invalid health probe interval %s or timeout %s: both must be above 0", o.Interval, o.Timeout)
	}
	if o.Failures < 1 {
		return fmt.Errorf("invalid health probe failures %d: must be 1 or more", o.Failures)
	}
	if len(o.Probes) == 0 {
		log.Printf("The server has no backend health probe, its health status does not follow the backend")
		<-ctx.Done()
		return nil
	}
	services := o.Services
	if services == nil {
		services = []string{"", pb.ABFS_ServiceDesc.ServiceName}
	}
	provider := o.Metrics
	if provider == nil {
		provider = metrics.GetProvider()
	}
	servingGauge := provider.NewGauge("abfs_server_serving",
		"1 while the backend health probe reports SERVING, 0 when it reports NOT_SERVING or the server shuts down.", nil)
	backendGauge := provider.NewGauge("abfs_server_backend_up",
		"1 when the backend's check passed in the last backend health probe, 0 when it failed or did not return in time.", []string{"backend"})
	servingGauge.Set(1)

	names := make([]string, 0, len(o.Probes))
	for name := range o.Probes {
		names = append(names, name)
	}
	slices.Sort(names)

	tick := o.tick
	if tick == nil {
		ticker := time.NewTicker(o.Interval)
		defer ticker.Stop()
		tick = ticker.C
	}

	// Only one round runs at a time, so the result channel never blocks a
	// round, also after this function returned.
	done := make(chan *probeRound, 1)
	var running *probeRound
	abandoned := false
	nextID := 0
	startRound := func() {
		nextID++
		running = &probeRound{id: nextID, start: time.Now(), errs: make(map[string]error, len(names))}
		abandoned = false
		go runProbeRound(ctx, running, o.Probes, o.Timeout, done)
	}

	// The outcomes of the last 2F-1 rounds, oldest first.
	windowSize := 2*o.Failures - 1
	window := make([]bool, 0, windowSize)
	passesInRow := 0
	serving := true
	record := func(passed bool) {
		if len(window) == windowSize {
			copy(window, window[1:])
			window = window[:windowSize-1]
		}
		window = append(window, passed)
		if passed {
			passesInRow++
		} else {
			passesInRow = 0
		}
	}
	failedCount := func() int {
		n := 0
		for _, passed := range window {
			if !passed {
				n++
			}
		}
		return n
	}
	setStatus := func(status grpc_health_v1.HealthCheckResponse_ServingStatus) {
		for _, service := range services {
			// The health service ignores this after Shutdown, so a drain
			// keeps reporting NOT_SERVING.
			o.Health.SetServingStatus(service, status)
		}
	}
	// applyFailure reports NOT_SERVING when a recorded failure reaches the
	// failure rule.
	applyFailure := func(cause string) {
		if serving && failedCount() >= o.Failures {
			serving = false
			setStatus(grpc_health_v1.HealthCheckResponse_NOT_SERVING)
			servingGauge.Set(0)
			log.Printf("Reporting NOT_SERVING: %d of the last %d backend health probes failed: %s", failedCount(), windowSize, cause)
		}
	}
	notify := func(e probeEvent) {
		if o.recorded != nil {
			o.recorded(e)
		}
	}

	// finish records the result of a round that has returned.
	finish := func(r *probeRound) {
		running = nil
		elapsed := elapsedSince(r.start)
		finished, _ := r.results(names)
		var failed []string
		for _, name := range names {
			if err := finished[name]; err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			}
		}
		if abandoned {
			result := "passed"
			if len(failed) > 0 {
				result = strings.Join(failed, "; ")
			}
			log.Printf("Backend health probe returned after %s, result discarded: %s", elapsed, result)
			notify(probeDiscarded)
			return
		}
		for _, name := range names {
			up := 0.0
			if finished[name] == nil {
				up = 1
			}
			backendGauge.Set(up, name)
		}
		if len(failed) > 0 {
			record(false)
			cause := strings.Join(failed, "; ")
			log.Printf("Backend health probe failed after %s (%d of the last %d failed): %s", elapsed, failedCount(), windowSize, cause)
			applyFailure(cause)
			notify(probeFailed)
			return
		}
		record(true)
		if !serving && passesInRow >= o.Failures {
			serving = true
			setStatus(grpc_health_v1.HealthCheckResponse_SERVING)
			servingGauge.Set(1)
			log.Printf("Reporting SERVING: %d backend health probes passed in a row", passesInRow)
		}
		notify(probePassed)
	}

	startRound()
	for {
		select {
		case <-ctx.Done():
			servingGauge.Set(0)
			return nil

		case <-tick:
			if running != nil {
				finished, waiting := running.results(names)
				if len(waiting) > 0 {
					// The round has not returned: count it as failed once
					// per tick, and discard its result when it comes.
					abandoned = true
					for _, name := range names {
						up := 0.0
						if err, ok := finished[name]; ok && err == nil {
							up = 1
						}
						backendGauge.Set(up, name)
					}
					record(false)
					cause := "waiting for " + strings.Join(waiting, ", ")
					log.Printf("Backend health probe still running after %s, counted as failed (%d of the last %d failed): %s",
						elapsedSince(running.start), failedCount(), windowSize, cause)
					applyFailure(cause)
					notify(probeStillRunning)
					continue
				}
				// Every check has returned, but the tick won the select
				// against done, or a starved watcher woke after both. Record
				// the round first so that it never counts as still running.
				// runProbeRound sends it right after its last check, so this
				// receive never waits on a check.
				finish(<-done)
			}
			startRound()

		case r := <-done:
			finish(r)
		}
	}
}

// elapsedSince returns the time since start, rounded for a log line.
func elapsedSince(start time.Time) time.Duration {
	d := time.Since(start)
	if d < time.Second {
		return d.Round(time.Microsecond)
	}
	return d.Round(time.Millisecond)
}

// runProbeRound runs every check of r in parallel under one timeout and sends
// r to done when all have returned. A check that returns after the timeout
// fails, also when it returns nil.
func runProbeRound(ctx context.Context, r *probeRound, probes map[string]func(context.Context) error, timeout time.Duration, done chan<- *probeRound) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var wg sync.WaitGroup
	for name, probe := range probes {
		wg.Go(func() {
			err := probe(ctx)
			if err == nil && (ctx.Err() != nil || !time.Now().Before(deadline)) {
				err = fmt.Errorf("no answer within %s", timeout)
			}
			r.set(name, err)
		})
	}
	wg.Wait()
	done <- r
}
