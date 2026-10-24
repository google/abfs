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

package client

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"syscall"
	"time"
)

const (
	defaultMaxIdleConnsPerHost = 32
	idleConnTimeout            = 90 * time.Second
	dialRetryInitialWait       = 10 * time.Millisecond
	dialRetryMaxWait           = time.Second
	// defaultDialRetryBudget bounds the retries while the listen queue is full: a unix
	// connect then fails at once with EAGAIN, which happens under a burst of cache checks.
	defaultDialRetryBudget = 30 * time.Second
	// defaultDialRetryRestartBudget bounds the retries while nothing listens, which only
	// lasts while the daemon restarts.
	defaultDialRetryRestartBudget = 3 * time.Second
)

var maxIdleConnsPerHost = defaultMaxIdleConnsPerHost

// SetMaxIdleConnsPerHost sets the idle connection pool of the transports created after
// the call. Clients that run many requests in parallel, like ninjago, raise it so that
// finished requests leave their connection for the next one.
func SetMaxIdleConnsPerHost(n int) {
	if n > 0 {
		maxIdleConnsPerHost = n
	}
}

// NewUnixTransport returns an http.Transport for a local daemon listening on socketPath.
func NewUnixTransport(socketPath string) *http.Transport {
	d := newUnixDialer(socketPath)
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.dial(ctx)
		},
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		IdleConnTimeout:     idleConnTimeout,
	}
}

// unixDialer dials a local daemon's unix socket.
type unixDialer struct {
	path          string
	budget        time.Duration
	restartBudget time.Duration
}

func newUnixDialer(path string) *unixDialer {
	return &unixDialer{path: path, budget: defaultDialRetryBudget, restartBudget: defaultDialRetryRestartBudget}
}

// dial retries while the listen queue is full or nothing listens yet. A failed dial
// sends nothing, so retrying it never repeats a request.
func (d *unixDialer) dial(ctx context.Context) (net.Conn, error) {
	var dialer net.Dialer
	start := time.Now()
	wait := dialRetryInitialWait
	for {
		conn, err := dialer.DialContext(ctx, "unix", d.path)
		if err == nil {
			return conn, nil
		}
		budget := d.budgetFor(err)
		if budget == 0 {
			return nil, err
		}
		jittered := wait/2 + rand.N(wait)
		if time.Since(start)+jittered > budget {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(jittered):
		}
		wait = min(2*wait, dialRetryMaxWait)
	}
}

func (d *unixDialer) budgetFor(err error) time.Duration {
	switch {
	case errors.Is(err, syscall.EAGAIN):
		return d.budget
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ENOENT):
		return d.restartBudget
	default:
		return 0
	}
}
