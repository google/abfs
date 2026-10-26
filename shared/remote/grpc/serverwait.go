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

package grpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
)

// serverWaitPause is the pause between two attempts to reach an unreachable server.
// gRPC's own retry policy already backs off within each attempt.
var serverWaitPause = time.Second

// OutageClock tracks how long a server has been unreachable. All calls to the
// server share one clock, so calls queued behind each other stop waiting at the
// same moment instead of each waiting the full timeout in turn.
// A nil clock or a zero timeout means calls do not wait.
type OutageClock struct {
	timeout time.Duration
	// conn is the connection to the server, set when it is dialed.
	conn interface{ GetState() connectivity.State }

	mu    sync.Mutex
	since time.Time
}

// NewOutageClock creates an OutageClock that lets calls wait up to timeout for an unreachable server.
func NewOutageClock(timeout time.Duration) *OutageClock {
	return &OutageClock{timeout: timeout}
}

func (c *OutageClock) enabled() bool {
	return c != nil && c.timeout > 0
}

// connState returns the state of the connection, or Ready when the clock has none.
func (c *OutageClock) connState() connectivity.State {
	if c == nil || c.conn == nil {
		return connectivity.Ready
	}
	return c.conn.GetState()
}

// gaveUp reports whether the timeout has passed while gRPC knows the server is down, so a call
// can fail without trying. gRPC keeps reconnecting meanwhile, and the first call after the
// connection leaves that state tries again.
func (c *OutageClock) gaveUp() bool {
	if !c.enabled() || c.connState() != connectivity.TransientFailure {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.since.IsZero() && time.Since(c.since) >= c.timeout
}

// record updates the clock after a call that did not fail with Unavailable.
func (c *OutageClock) record(err error) {
	if c == nil {
		return
	}
	switch status.Code(err) {
	case codes.OK:
		c.Reached()
	case codes.DeadlineExceeded:
		// A call that ran out of time while gRPC was still connecting did not reach the
		// server, as when the server's address drops packets.
		if c.connState() != connectivity.Ready {
			c.failed()
		}
	case codes.Canceled:
		// The caller gave up, which says nothing about the server.
	default:
		// Errors raised on the client, such as credentials that cannot be fetched, do not
		// mean that the server answered.
		if c.connState() == connectivity.Ready {
			c.Reached()
		}
	}
}

// Reached records that the server answered.
func (c *OutageClock) Reached() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.since = time.Time{}
	c.mu.Unlock()
}

// Waiting reports whether the server is unreachable and calls still wait for it.
func (c *OutageClock) Waiting() bool {
	if c == nil || c.timeout <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.since.IsZero() && time.Since(c.since) < c.timeout
}

// failed records that the server could not be reached and returns when calls stop waiting.
func (c *OutageClock) failed() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.since.IsZero() {
		c.since = time.Now()
	}
	return c.since.Add(c.timeout)
}

func (c *OutageClock) downFor() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.since.IsZero() {
		return 0
	}
	return time.Since(c.since)
}

// waitForServer records a failed attempt and pauses before the next one. It
// returns false when the caller should give up.
func (c *OutageClock) waitForServer(ctx context.Context) bool {
	if c == nil {
		return false
	}
	// Recorded before the checks below, so a health check that does not wait still starts the clock.
	deadline := c.failed()
	if c.timeout <= 0 || ctx.Value(noServerWaitKey{}) != nil {
		return false
	}
	pause := min(serverWaitPause, time.Until(deadline))
	if pause <= 0 {
		return false
	}
	t := time.NewTimer(pause)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// unreachable adds how long the server has been unreachable to err, keeping its gRPC code.
func (c *OutageClock) unreachable(ctx context.Context, err error) error {
	if c == nil || c.timeout <= 0 || ctx.Value(noServerWaitKey{}) != nil {
		return err
	}
	return fmt.Errorf("server unreachable for %s: %w", c.downFor().Round(time.Second), err)
}

var errTransientFailure = status.Error(codes.Unavailable, "connection to the server is in transient failure")

type noServerWaitKey struct{}

// WithoutServerWait returns a context whose calls fail at once when the server is unreachable.
// Health checks use it so they report an outage without waiting for it to end.
func WithoutServerWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, noServerWaitKey{}, true)
}

func serverWaitInterceptor(serviceName string, clock *OutageClock) grpc.UnaryClientInterceptor {
	servicePrefix := "/" + serviceName
	return func(
		ctx context.Context,
		method string,
		req any,
		reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if !strings.HasPrefix(method, servicePrefix) {
			return invoker(ctx, method, req, reply, cc, opts...)
		}
		if clock.gaveUp() {
			return clock.unreachable(ctx, errTransientFailure)
		}
		for {
			err := invoker(ctx, method, req, reply, cc, opts...)
			if status.Code(err) != codes.Unavailable {
				clock.record(err)
				return err
			}
			if !clock.waitForServer(ctx) {
				return clock.unreachable(ctx, err)
			}
		}
	}
}
