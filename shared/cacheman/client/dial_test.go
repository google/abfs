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
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
)

// bindUnix binds a unix socket without listening on it. net.Listen always asks for
// somaxconn, so the tests use a raw socket to control the listen queue.
func bindUnix(t *testing.T, path string) int {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	require.NoError(t, syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}))
	return fd
}

func fileListener(fd int) (net.Listener, error) {
	f := os.NewFile(uintptr(fd), "listener")
	defer func() { _ = f.Close() }()
	return net.FileListener(f)
}

func listenUnix(t *testing.T, path string, backlog int) net.Listener {
	t.Helper()
	fd := bindUnix(t, path)
	require.NoError(t, syscall.Listen(fd, backlog))
	l, err := fileListener(fd)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// fillQueue dials path until the kernel refuses with EAGAIN, so the next dial finds the
// listen queue full.
func fillQueue(t *testing.T, path string) {
	t.Helper()
	for range 16 {
		conn, err := net.Dial("unix", path)
		if errors.Is(err, syscall.EAGAIN) {
			return
		}
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
	}
	t.Fatal("listen queue never filled")
}

func mapResultsHandler(delay time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		_ = json.NewEncoder(w).Encode([]string{})
	})
}

func serve(l net.Listener, h http.Handler) {
	go func() { _ = http.Serve(l, h) }()
}

func TestHTTPClient_RetriesFullListenQueue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "http.socket")
	l := listenUnix(t, path, 1)
	fillQueue(t, path)
	time.AfterFunc(200*time.Millisecond, func() { serve(l, mapResultsHandler(0)) })

	_, err := NewHTTPClient(dir).MapResults(entities.Hash{})
	require.NoError(t, err)
}

func TestHTTPClient_RetriesUntilListening(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "http.socket")
	fd := bindUnix(t, path)

	var mu sync.Mutex
	var l net.Listener
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if l != nil {
			_ = l.Close()
		}
	})
	time.AfterFunc(300*time.Millisecond, func() {
		mu.Lock()
		defer mu.Unlock()
		if err := syscall.Listen(fd, 16); err != nil {
			return
		}
		var err error
		if l, err = fileListener(fd); err == nil {
			serve(l, mapResultsHandler(0))
		}
	})

	_, err := NewHTTPClient(dir).MapResults(entities.Hash{})
	require.NoError(t, err)
}

func TestHTTPClient_TimeoutDuringDialRetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "http.socket")
	listenUnix(t, path, 1)
	fillQueue(t, path)

	// The transport keeps dialing after the request gives up, so a dial goroutine outlives
	// the test until the cleanup closes the listener and the restart budget ends it.
	start := time.Now()
	_, err := NewHTTPClientWithTimeout(dir, 200*time.Millisecond).MapResults(entities.Hash{})
	elapsed := time.Since(start)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.GreaterOrEqual(t, elapsed, 150*time.Millisecond)
	require.Less(t, elapsed, 5*time.Second)
}

type countingListener struct {
	net.Listener
	accepted atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

func TestHTTPClient_ReusesIdleConnections(t *testing.T) {
	dir := t.TempDir()
	l, err := net.Listen("unix", filepath.Join(dir, "http.socket"))
	require.NoError(t, err)
	cl := &countingListener{Listener: l}
	t.Cleanup(func() { _ = cl.Close() })
	serve(cl, mapResultsHandler(20*time.Millisecond))

	c := NewHTTPClient(dir)
	const batches, concurrent = 5, 8
	for range batches {
		var grp errgroup.Group
		for range concurrent {
			grp.Go(func() error {
				_, err := c.MapResults(entities.Hash{})
				return err
			})
		}
		require.NoError(t, grp.Wait())
	}
	require.LessOrEqual(t, int(cl.accepted.Load()), 2*concurrent)
}

func TestUnixDialer_BudgetFor(t *testing.T) {
	opErr := func(errno syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", errno)}
	}
	d := &unixDialer{budget: time.Minute, restartBudget: time.Second}
	tests := []struct {
		name string
		err  error
		want time.Duration
	}{
		{name: "full queue", err: opErr(syscall.EAGAIN), want: time.Minute},
		{name: "no listener", err: opErr(syscall.ECONNREFUSED), want: time.Second},
		{name: "no socket file", err: opErr(syscall.ENOENT), want: time.Second},
		{name: "permission denied", err: opErr(syscall.EACCES), want: 0},
		{name: "context", err: context.Canceled, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, d.budgetFor(tt.err))
		})
	}
}

func TestUnixDialer_GivesUpAfterBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "http.socket")
	listenUnix(t, path, 1)
	fillQueue(t, path)
	d := &unixDialer{path: path, budget: 300 * time.Millisecond, restartBudget: 300 * time.Millisecond}

	start := time.Now()
	_, err := d.dial(context.Background())
	elapsed := time.Since(start)
	require.ErrorIs(t, err, syscall.EAGAIN)
	// The loop gives up before a wait that would pass the budget, so it can return well
	// before 300 ms; four minimal waits already take 75 ms.
	require.GreaterOrEqual(t, elapsed, 50*time.Millisecond)
	require.Less(t, elapsed, 2*time.Second)
}

func TestUnixDialer_MissingSocketUsesRestartBudget(t *testing.T) {
	d := &unixDialer{path: filepath.Join(t.TempDir(), "http.socket"), budget: time.Minute, restartBudget: 200 * time.Millisecond}

	start := time.Now()
	_, err := d.dial(context.Background())
	require.ErrorIs(t, err, syscall.ENOENT)
	require.Less(t, time.Since(start), 2*time.Second)
}

func TestUnixDialer_CancelStopsRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "http.socket")
	listenUnix(t, path, 1)
	fillQueue(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, err := newUnixDialer(path).dial(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 2*time.Second)
}

func TestSetMaxIdleConnsPerHost(t *testing.T) {
	old := maxIdleConnsPerHost
	t.Cleanup(func() { maxIdleConnsPerHost = old })

	SetMaxIdleConnsPerHost(0)
	require.Equal(t, old, NewUnixTransport("x").MaxIdleConnsPerHost)
	SetMaxIdleConnsPerHost(2600)
	require.Equal(t, 2600, NewUnixTransport("x").MaxIdleConnsPerHost)
}
