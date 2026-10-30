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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
)

const testMethod = "/abfs.ABFS/Headers"

// fakeInvoker returns the errors in turn, then nil, and counts the calls.
type fakeInvoker struct {
	errs  []error
	calls int
}

func (f *fakeInvoker) invoke(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
	f.calls++
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	if len(f.errs) > 1 {
		f.errs = f.errs[1:]
	}
	return err
}

func unavailable() error {
	return status.Error(codes.Unavailable, "connection refused")
}

func withPause(t *testing.T, d time.Duration) {
	old := serverWaitPause
	serverWaitPause = d
	t.Cleanup(func() { serverWaitPause = old })
}

func TestServerWaitInterceptor_WaitsForServer(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	clock := NewOutageClock(time.Second)
	inv := &fakeInvoker{errs: []error{unavailable(), unavailable(), unavailable(), nil}}

	err := serverWaitInterceptor("abfs.ABFS", clock)(context.Background(), testMethod, nil, nil, nil, inv.invoke)

	require.NoError(t, err)
	require.Equal(t, 4, inv.calls)
	require.False(t, clock.Waiting())
}

func TestServerWaitInterceptor_GivesUpAfterTimeout(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	clock := NewOutageClock(100 * time.Millisecond)
	inv := &fakeInvoker{errs: []error{unavailable()}}

	start := time.Now()
	err := serverWaitInterceptor("abfs.ABFS", clock)(context.Background(), testMethod, nil, nil, nil, inv.invoke)

	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Contains(t, err.Error(), "server unreachable for")
	require.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond)
	require.Greater(t, inv.calls, 1)
}

func TestServerWaitInterceptor_SharedClock(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	clock := NewOutageClock(100 * time.Millisecond)
	interceptor := serverWaitInterceptor("abfs.ABFS", clock)
	first := &fakeInvoker{errs: []error{unavailable()}}
	require.Error(t, interceptor(context.Background(), testMethod, nil, nil, nil, first.invoke))

	// The outage outlasted the timeout: a new call tries once and does not wait again.
	second := &fakeInvoker{errs: []error{unavailable()}}
	start := time.Now()
	err := interceptor(context.Background(), testMethod, nil, nil, nil, second.invoke)

	require.Error(t, err)
	require.Equal(t, 1, second.calls)
	require.Less(t, time.Since(start), 50*time.Millisecond)
}

func TestServerWaitInterceptor_AnswerResetsClock(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	clock := NewOutageClock(100 * time.Millisecond)
	interceptor := serverWaitInterceptor("abfs.ABFS", clock)
	require.Error(t, interceptor(context.Background(), testMethod, nil, nil, nil, (&fakeInvoker{errs: []error{unavailable()}}).invoke))

	notFound := status.Error(codes.NotFound, "no such object")
	inv := &fakeInvoker{errs: []error{notFound}}
	require.Equal(t, codes.NotFound, status.Code(interceptor(context.Background(), testMethod, nil, nil, nil, inv.invoke)))
	require.Equal(t, 1, inv.calls)

	// The server answered, so the next outage gets a full timeout again.
	again := &fakeInvoker{errs: []error{unavailable(), nil}}
	require.NoError(t, interceptor(context.Background(), testMethod, nil, nil, nil, again.invoke))
	require.Equal(t, 2, again.calls)
}

func TestServerWaitInterceptor_NoWait(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	tests := []struct {
		name   string
		clock  *OutageClock
		ctx    context.Context
		method string
	}{
		{"nil clock", nil, context.Background(), testMethod},
		{"zero timeout", NewOutageClock(0), context.Background(), testMethod},
		{"without server wait", NewOutageClock(time.Minute), WithoutServerWait(context.Background()), testMethod},
		{"other service", NewOutageClock(time.Minute), context.Background(), "/grpc.health.v1.Health/Check"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := &fakeInvoker{errs: []error{unavailable()}}
			err := serverWaitInterceptor("abfs.ABFS", tt.clock)(tt.ctx, tt.method, nil, nil, nil, inv.invoke)
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.NotContains(t, err.Error(), "server unreachable for")
			require.Equal(t, 1, inv.calls)
		})
	}
}

func TestServerWaitInterceptor_OtherErrorsNotRetried(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	clock := NewOutageClock(time.Minute)
	inv := &fakeInvoker{errs: []error{status.Error(codes.Internal, "boom")}}

	err := serverWaitInterceptor("abfs.ABFS", clock)(context.Background(), testMethod, nil, nil, nil, inv.invoke)

	require.Equal(t, codes.Internal, status.Code(err))
	require.Equal(t, 1, inv.calls)
}

func TestServerWaitInterceptor_ContextCanceled(t *testing.T) {
	withPause(t, time.Minute)
	clock := NewOutageClock(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	inv := &fakeInvoker{errs: []error{unavailable()}}

	start := time.Now()
	err := serverWaitInterceptor("abfs.ABFS", clock)(ctx, testMethod, nil, nil, nil, inv.invoke)

	require.Error(t, err)
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, 1, inv.calls)
}

type fakeConn struct {
	state connectivity.State
}

func (f *fakeConn) GetState() connectivity.State {
	return f.state
}

func TestOutageClock_GaveUp(t *testing.T) {
	tests := []struct {
		state connectivity.State
		want  bool
	}{
		{connectivity.Idle, false},
		{connectivity.Connecting, false},
		{connectivity.Ready, false},
		{connectivity.TransientFailure, true},
	}
	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			clock := NewOutageClock(time.Millisecond)
			clock.conn = &fakeConn{state: tt.state}
			clock.failed()
			time.Sleep(5 * time.Millisecond)
			require.Equal(t, tt.want, clock.gaveUp())
		})
	}

	// Within the timeout calls still try.
	clock := NewOutageClock(time.Hour)
	clock.conn = &fakeConn{state: connectivity.TransientFailure}
	clock.failed()
	require.False(t, clock.gaveUp())
}

func TestOutageClock_Record(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		state    connectivity.State
		wasDown  bool
		wantDown bool
	}{
		{"deadline while connecting starts", status.Error(codes.DeadlineExceeded, "x"), connectivity.Connecting, false, true},
		{"deadline while ready does not start", status.Error(codes.DeadlineExceeded, "x"), connectivity.Ready, false, false},
		{"canceled while connecting does not start", status.Error(codes.Canceled, "x"), connectivity.Connecting, false, false},
		{"canceled does not reset", status.Error(codes.Canceled, "x"), connectivity.Ready, true, true},
		{"success resets", nil, connectivity.Ready, true, false},
		{"error from a connected server resets", status.Error(codes.Unauthenticated, "x"), connectivity.Ready, true, false},
		{"client error while down does not reset", status.Error(codes.Unauthenticated, "x"), connectivity.TransientFailure, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := NewOutageClock(time.Hour)
			clock.conn = &fakeConn{state: tt.state}
			if tt.wasDown {
				clock.failed()
			}
			clock.record(tt.err)
			require.Equal(t, tt.wantDown, clock.Waiting())
		})
	}
}
