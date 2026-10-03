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

package metrics

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"os/user"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/google/abfs/shared/version"
)

type ProviderFactory func(enabled bool) Provider

var (
	mu        sync.RWMutex
	factories = make(map[string]ProviderFactory)
)

// Register registers a metrics provider factory by name.
func Register(name string, factory ProviderFactory) {
	mu.Lock()
	defer mu.Unlock()
	if factory == nil {
		panic("metrics: Register factory is nil")
	}
	if _, dup := factories[name]; dup {
		panic("metrics: Register called twice for provider " + name)
	}
	factories[name] = factory
}

// NewProvider instantiates a metrics provider by name. If not found, it prints a warning and returns a Noop provider.
func NewProvider(name string, enabled bool) Provider {
	mu.RLock()
	defer mu.RUnlock()
	factory, ok := factories[name]
	if !ok {
		log.Printf("Warning: metrics: unknown provider %q (forgot anonymous import?), falling back to Noop.\n", name)
		return NewNoopProvider()
	}
	return factory(enabled)
}

// Counter represents a metric that only goes up.
type Counter interface {
	// Inc increase the counter by 1.
	Inc(labelValues ...string)

	// Add adds the given value to the counter. It panics if the value is < 0.
	Add(val float64, labelValues ...string)
}

// Gauge represents a metric that can go up and down.
type Gauge interface {
	// Set forces the Gauge to a specific value.
	Set(val float64, labelValues ...string)

	// Add applies a relative adjustment to the Gauge.
	// Unlike Counter, Gauge allows negative value to be passed to Add.
	Add(val float64, labelValues ...string)
}

// Histogram represents a metric that samples observations.
type Histogram interface {
	// Observe records a single data point (e.g., the time taken to process
	// a request) into the histogram's underlying buckets.
	Observe(val float64, labelValues ...string)
}

// Provider is the unified factory for creating metrics.
// Clients use this interface to create all metrics, including Timers
type Provider interface {
	// NewCounter creates a new metric that strictly increases (e.g., number of requests served,
	// errors encountered, or tasks completed).
	NewCounter(name, help string, labels []string) Counter

	// NewGauge creates a new metric that can arbitrarily go up and down (e.g., current memory usage,
	// number of active connections, or queue size).
	NewGauge(name, help string, labels []string) Gauge

	// NewHistogram creates a new metric that samples observations (usually request durations or response sizes)
	// and counts them in configurable buckets.
	NewHistogram(name, help string, buckets []float64, labels []string) Histogram

	// NewTimer creates a specialized component specifically designed for measuring durations.
	// It wraps an underlying Histogram and provides a convenient Stop() method to record elapsed time.
	NewTimer(name, help string, buckets []float64, labels []string) *Timer

	// Push forces the underlying metrics to be sent to the remote backend immediately.
	// This is typically used in batch jobs, ephemeral processes, or when using a push-based
	// architecture.
	Push(ctx context.Context) error

	// Close flushes pending metrics.
	Close(ctx context.Context) error
}

// Backend is the engine API.
// Implementers (e.g., Prometheus) only need to implement these methods.
type Backend interface {
	// NewCounter must return a thread-safe Counter implementation native to the backend.
	NewCounter(name, help string, labels []string) Counter

	// NewGauge must return a thread-safe Gauge implementation native to the backend.
	NewGauge(name, help string, labels []string) Gauge

	// NewHistogram must return a thread-safe Histogram implementation native to the backend.
	// The implementation should map the provided bucket boundaries to the backend's native
	// histogram structure.
	NewHistogram(name, help string, buckets []float64, labels []string) Histogram

	// Push should implement the logic to push metrics to a remote aggregator.
	// If the backend operates on a purely pull-based model,
	// this can safely be implemented as a no-op returning nil.
	Push(ctx context.Context) error

	// Close should gracefully flush any in-memory buffers, stop background synchronization loops (if any),
	// and close open connections to the metrics sink (if any).
	Close(ctx context.Context) error
}

var globalProvider atomic.Pointer[Provider]

func init() {
	defaultProvider := WrapBackend(&noopBackend{})
	globalProvider.Store(&defaultProvider)
}

func SetProvider(p Provider) {
	if p != nil {
		globalProvider.Store(&p)
	}
}

func GetProvider() Provider {
	return *globalProvider.Load()
}

type providerWrapper struct {
	Backend // Promote Backend's methods
}

func (p *providerWrapper) NewTimer(name, help string, buckets []float64, labels []string) *Timer {
	hist := p.NewHistogram(name, help, buckets, labels)

	return &Timer{
		histogram:   hist,
		start:       time.Now(),
		labelValues: labels,
	}
}

// WrapBackend takes a bare Backend and wraps it with default implementations (like Timer).
func WrapBackend(b Backend) Provider {
	return &providerWrapper{
		Backend: b,
	}
}

const usernameSalt = "abfs-telemetry-salt-2026"

var (
	activeInstanceName  string
	isDaemonProcess     bool
	metricsRunID        = uuid.NewString() // Stable across the entire lifespan of CLI execution
	metricsUsernameHash = getUsernameHash()
	metricsStateLock    sync.RWMutex
)

func SetInstanceName(name string) {
	metricsStateLock.Lock()
	defer metricsStateLock.Unlock()
	activeInstanceName = name
}

func SetDaemon(daemon bool) {
	metricsStateLock.Lock()
	defer metricsStateLock.Unlock()
	isDaemonProcess = daemon
}

func IsDaemon() bool {
	metricsStateLock.RLock()
	defer metricsStateLock.RUnlock()
	return isDaemonProcess
}

func getUsernameHash() string {
	rawUser := "unknown"
	if u, err := user.Current(); err == nil {
		rawUser = u.Username
	} else if u := os.Getenv("USER"); u != "" {
		rawUser = u
	}

	if rawUser == "unknown" {
		return "unknown"
	}

	mac := hmac.New(sha256.New, []byte(usernameSalt))
	mac.Write([]byte(rawUser))
	return hex.EncodeToString(mac.Sum(nil))
}

// DefaultLabels returns a map of standard environmental labels that should
// be applied to all metrics across the ABFS fleet, regardless of the backend provider.
func DefaultLabels() map[string]string {
	h, err := os.Hostname()
	if err != nil {
		h = "unknown"
	}

	metricsStateLock.RLock()
	name := activeInstanceName
	metricsStateLock.RUnlock()

	labels := map[string]string{
		"hostname": h,
		"version":  version.Version(),
		"runID":    metricsRunID,
		"username": metricsUsernameHash,
	}
	if name != "" {
		labels["abfs_instance"] = name
	}
	return labels
}

// AndroidTarget returns the combined target product and build variant from environment variables, e.g. "product-variant", or "unknown" if they are missing.
func AndroidTarget() string {
	targetProduct := os.Getenv("TARGET_PRODUCT")
	buildVariant := os.Getenv("TARGET_BUILD_VARIANT")
	if targetProduct != "" && buildVariant != "" {
		return targetProduct + "-" + buildVariant
	}
	return "unknown"
}

// NewNoopProvider returns a non-operational metrics provider.
func NewNoopProvider() Provider {
	return WrapBackend(&noopBackend{})
}

// LazyProvider delays the creation of the underlying provider until it's actually used.
type LazyProvider struct {
	initOnce sync.Once
	factory  func() Provider
	provider atomic.Value
}

// NewLazyProvider creates a provider that delays calling the factory until a metric is created.
func NewLazyProvider(factory func() Provider) Provider {
	return &LazyProvider{factory: factory}
}

func (l *LazyProvider) init() {
	l.initOnce.Do(func() {
		p := l.factory()
		if p == nil {
			p = WrapBackend(&noopBackend{})
		}
		l.provider.Store(p)
	})
}

func (l *LazyProvider) get() Provider {
	l.init()
	return l.provider.Load().(Provider)
}

func (l *LazyProvider) NewCounter(name, help string, labels []string) Counter {
	return l.get().NewCounter(name, help, labels)
}

func (l *LazyProvider) NewGauge(name, help string, labels []string) Gauge {
	return l.get().NewGauge(name, help, labels)
}

func (l *LazyProvider) NewHistogram(name, help string, buckets []float64, labels []string) Histogram {
	return l.get().NewHistogram(name, help, buckets, labels)
}

func (l *LazyProvider) NewTimer(name, help string, buckets []float64, labels []string) *Timer {
	return l.get().NewTimer(name, help, buckets, labels)
}

func (l *LazyProvider) Push(ctx context.Context) error {
	p := l.provider.Load()
	if p == nil {
		return nil
	}
	return p.(Provider).Push(ctx)
}

func (l *LazyProvider) Close(ctx context.Context) error {
	p := l.provider.Load()
	if p == nil {
		return nil
	}
	return p.(Provider).Close(ctx)
}

type Timer struct {
	histogram   Histogram
	start       time.Time
	labelValues []string
}

// Stop record the duration into the underlying histogram
func (t *Timer) Stop() time.Duration {
	duration := time.Since(t.start)
	t.histogram.Observe(duration.Seconds(), t.labelValues...)
	return duration
}

type noopBackend struct{}

func (n *noopBackend) NewCounter(name, help string, labels []string) Counter {
	return &noopCounter{}
}
func (n *noopBackend) NewGauge(name, help string, labels []string) Gauge {
	return &noopGauge{}
}
func (n *noopBackend) NewHistogram(name, help string, buckets []float64, labels []string) Histogram {
	return &noopHistogram{}
}
func (n *noopBackend) Push(ctx context.Context) error  { return nil }
func (n *noopBackend) Close(ctx context.Context) error { return nil }

type noopCounter struct{}

func (n *noopCounter) Inc(labelValues ...string)              {}
func (n *noopCounter) Add(val float64, labelValues ...string) {}

type noopGauge struct{}

func (n *noopGauge) Set(val float64, labelValues ...string) {}
func (n *noopGauge) Add(val float64, labelValues ...string) {}

type noopHistogram struct{}

func (n *noopHistogram) Observe(val float64, labelValues ...string) {}
