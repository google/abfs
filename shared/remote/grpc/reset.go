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
	"log"
	"math/rand/v2"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/abfslog"
)

// The attempts and backoff are those of the retry policy of the client connections, and each wait
// varies by -20% to +20% as grpc-go varies the waits of that policy.
const (
	resetAttempts   = 5
	resetMaxBackoff = 5 * time.Second
)

// resetInitialBackoff is a variable so tests can shorten it.
var resetInitialBackoff = 500 * time.Millisecond

// resetLog limits the retry lines, as a proxy can reset many streams at once.
var resetLog = abfslog.NewLimiter("stream reset retry")

// isStreamReset reports whether err is an HTTP/2 stream reset by the peer. A proxy between client and
// server, such as a load balancer, resets streams when it drops a connection. grpc-go reports a reset
// as Internal (INTERNAL_ERROR and most other codes) or Canceled (CANCEL) with this message. A status
// the server returns keeps its own message, and a call the client cancels ends with the context error,
// so neither is taken for a reset. A server error that quotes a reset in its message, such as one that
// wraps a reset of its own backend call, is taken for one; the retried calls are idempotent.
func isStreamReset(err error) bool {
	s, ok := status.FromError(err)
	if !ok || (s.Code() != codes.Internal && s.Code() != codes.Canceled) {
		return false
	}
	return strings.Contains(s.Message(), "stream terminated by RST_STREAM")
}

func resetBackoff(retry int) time.Duration {
	d := min(resetInitialBackoff<<(retry-1), resetMaxBackoff)
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// resetWaitError ends a wait after a reset when the call's context ends. Its status is that of the
// context error, as for any cancelled call, and its text also names the reset.
type resetWaitError struct {
	status *status.Status
	reset  error
}

func (e *resetWaitError) Error() string {
	return fmt.Sprintf("%v, while waiting to retry after %v", e.status.Err(), e.reset)
}

func (e *resetWaitError) GRPCStatus() *status.Status {
	return e.status
}

// waitAfterReset logs the reset and waits before the given retry. If ctx ends first, it returns the status
// of the context error.
func waitAfterReset(ctx context.Context, method string, retry int, reset error) error {
	resetLog.Printf(log.Default(), "Retrying %s after a stream reset (retry %d of %d): %v", method, retry, resetAttempts-1, reset)
	timer := time.NewTimer(resetBackoff(retry))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return &resetWaitError{status: status.FromContextError(ctx.Err()), reset: reset}
	case <-timer.C:
		return nil
	}
}

// callWithResetRetry calls f again after a stream reset, up to resetAttempts calls in all.
// Only idempotent calls may use it: the server may have completed a call whose reply was reset.
func callWithResetRetry[T any](ctx context.Context, method string, f func() (T, error)) (T, error) {
	for retry := 1; ; retry++ {
		resp, err := f()
		if !isStreamReset(err) || retry == resetAttempts {
			return resp, err
		}
		if err := waitAfterReset(ctx, method, retry, err); err != nil {
			var zero T
			return zero, err
		}
	}
}

type callContextKey struct{}

// withCallContext stores ctx in itself, so a download can still wait on its call after a reset:
// grpc-go cancels the context of the stream itself when the stream fails.
func withCallContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, callContextKey{}, ctx)
}

func callContext(stream Stream) context.Context {
	if ctx, ok := stream.Context().Value(callContextKey{}).(context.Context); ok {
		return ctx
	}
	return context.Background()
}

// reopenAfterReset reopens a download after a reset and skips the bytes already read. A reset of the
// reopened stream, also during the skip, uses another of the resetAttempts opens.
func (s *recvStream) reopenAfterReset(err error) error {
	ctx := callContext(s.stream)
	for isStreamReset(err) && s.resets < resetAttempts-1 {
		s.resets++
		if err := waitAfterReset(ctx, "GetObject", s.resets, err); err != nil {
			return err
		}
		err = s.reopenAndSkip()
	}
	return err
}
