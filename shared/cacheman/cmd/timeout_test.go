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

package cacheman

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	cachemanclient "github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/cmd"
)

// startHangingHTTPSocket listens on <dir>/http.socket and accepts connections without answering.
func startHangingHTTPSocket(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	lis, err := net.Listen("unix", filepath.Join(dir, "http.socket"))
	require.NoError(t, err)

	conns := make(chan net.Conn, 16)
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			conns <- conn
		}
	}()
	t.Cleanup(func() {
		_ = lis.Close()
		for {
			select {
			case conn := <-conns:
				_ = conn.Close()
			default:
				return
			}
		}
	})
	return dir
}

// startUnixHTTPServer serves handler on <dir>/http.socket.
func startUnixHTTPServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	dir := t.TempDir()
	lis, err := net.Listen("unix", filepath.Join(dir, "http.socket"))
	require.NoError(t, err)

	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = lis.Close()
	})
	return dir
}

func TestCachemanCLICommandsHonorTimeoutAndCancel(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		run     func(ctx context.Context, client *cachemanclient.Client, timeout time.Duration) error
	}{
		{
			name:    "ping",
			timeout: 200 * time.Millisecond,
			run: func(ctx context.Context, client *cachemanclient.Client, timeout time.Duration) error {
				c := &pingCmd{
					CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
					timeout:          timeout,
				}
				return c.ping(ctx)
			},
		},
		{
			name:    "remote-ok",
			timeout: 200 * time.Millisecond,
			run: func(ctx context.Context, client *cachemanclient.Client, timeout time.Duration) error {
				c := &remoteOkCmd{
					CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
					timeout:          timeout,
				}
				return c.remoteOk(ctx)
			},
		},
		{
			name:    "wait",
			timeout: time.Second,
			run: func(ctx context.Context, client *cachemanclient.Client, timeout time.Duration) error {
				c := &waitCmd{
					CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
					timeout:          int64(timeout / time.Second),
					httpMargin:       100 * time.Millisecond,
				}
				return c.run(ctx)
			},
		},
		{
			name:    "waitForCacheman",
			timeout: 200 * time.Millisecond,
			run: func(ctx context.Context, client *cachemanclient.Client, timeout time.Duration) error {
				return waitForCacheman(ctx, timeout, "remote-ok", client.RemoteOKContext)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" timeout", func(t *testing.T) {
			dir := startHangingHTTPSocket(t)
			client := cachemanclient.NewClient(dir, time.Second)
			t.Cleanup(func() { _ = client.Close() })

			start := time.Now()
			err := tt.run(context.Background(), client, tt.timeout)
			require.Error(t, err)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Less(t, time.Since(start), 3*time.Second)
		})

		t.Run(tt.name+" cancel", func(t *testing.T) {
			dir := startHangingHTTPSocket(t)
			client := cachemanclient.NewClient(dir, time.Second)
			t.Cleanup(func() { _ = client.Close() })

			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				time.Sleep(50 * time.Millisecond)
				cancel()
			}()

			start := time.Now()
			err := tt.run(ctx, client, 60*time.Second)
			require.ErrorIs(t, err, context.Canceled)
			require.Less(t, time.Since(start), time.Second)
		})
	}
}

func TestCachemanRejectNonPositiveTimeout(t *testing.T) {
	dir := t.TempDir()
	client := cachemanclient.NewClient(dir, time.Second)
	t.Cleanup(func() { _ = client.Close() })

	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run("ping_"+timeout.String(), func(t *testing.T) {
			c := &pingCmd{
				CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
				timeout:          timeout,
			}
			err := c.ping(context.Background())
			require.ErrorContains(t, err, "timeout must be positive")
		})
		t.Run("remote-ok_"+timeout.String(), func(t *testing.T) {
			c := &remoteOkCmd{
				CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
				timeout:          timeout,
			}
			err := c.remoteOk(context.Background())
			require.ErrorContains(t, err, "timeout must be positive")
		})
		t.Run("restart_"+timeout.String(), func(t *testing.T) {
			c := &restartCmd{timeout: timeout, sysRestart: &recordingCmd{}}
			err := c.Exec(context.Background(), pflag.NewFlagSet("restart", pflag.ContinueOnError))
			require.ErrorContains(t, err, "timeout must be positive")
			require.False(t, c.sysRestart.(*recordingCmd).ran)
		})
	}
}

// recordingCmd is a system restart command that records that it ran.
type recordingCmd struct {
	cmd.ABFSCmd
	ran bool
}

func (c *recordingCmd) Exec(context.Context, *pflag.FlagSet) error {
	c.ran = true
	return nil
}

func TestCachemanPingAndRemoteOKTimeoutWrapsLastError(t *testing.T) {
	const serverMsg = "starting up"
	unavailable := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, serverMsg, http.StatusServiceUnavailable)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", unavailable)
	mux.HandleFunc("/remote-ok", unavailable)
	dir := startUnixHTTPServer(t, mux)
	client := cachemanclient.NewClient(dir, time.Second)
	t.Cleanup(func() { _ = client.Close() })

	t.Run("ping", func(t *testing.T) {
		c := &pingCmd{
			CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
			timeout:          200 * time.Millisecond,
		}
		start := time.Now()
		err := c.ping(context.Background())
		require.ErrorContains(t, err, "timeout waiting for a successful ping")
		require.ErrorContains(t, err, serverMsg)
		require.Less(t, time.Since(start), 600*time.Millisecond)
	})

	t.Run("remote-ok", func(t *testing.T) {
		c := &remoteOkCmd{
			CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
			timeout:          200 * time.Millisecond,
		}
		start := time.Now()
		err := c.remoteOk(context.Background())
		require.ErrorContains(t, err, "timeout waiting for a successful remote check")
		require.ErrorContains(t, err, serverMsg)
		require.Less(t, time.Since(start), 600*time.Millisecond)
	})
}

func TestCachemanPingRetriesUntilSuccess(t *testing.T) {
	var attempts atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "starting up", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	dir := startUnixHTTPServer(t, mux)
	client := cachemanclient.NewClient(dir, time.Second)
	t.Cleanup(func() { _ = client.Close() })

	c := &pingCmd{
		CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
		timeout:          5 * time.Second,
	}
	require.NoError(t, c.ping(context.Background()))
	require.Equal(t, int32(2), attempts.Load())
}

func TestCachemanWaitReturnsServer408(t *testing.T) {
	const serverMsg = "timed out waiting for dagsync idle"
	mux := http.NewServeMux()
	mux.HandleFunc("/sync/wait", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1100 * time.Millisecond):
		}
		w.WriteHeader(http.StatusRequestTimeout)
		_, _ = w.Write([]byte(serverMsg))
	})
	dir := startUnixHTTPServer(t, mux)
	client := cachemanclient.NewClient(dir, time.Second)
	t.Cleanup(func() { _ = client.Close() })

	c := &waitCmd{
		CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
		timeout:          1,
		httpMargin:       2 * time.Second,
	}
	err := c.run(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, serverMsg)
	require.NotErrorIs(t, err, context.DeadlineExceeded)
}

func TestCachemanWaitMarginDefault(t *testing.T) {
	c := &waitCmd{}
	require.Equal(t, 5*time.Second, c.margin())

	c.httpMargin = 250 * time.Millisecond
	require.Equal(t, 250*time.Millisecond, c.margin())
}

func TestCachemanWaitRetryPolicy(t *testing.T) {
	t.Run("retries transient error until success", func(t *testing.T) {
		var attempts atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/sync/wait", func(w http.ResponseWriter, r *http.Request) {
			if attempts.Add(1) == 1 {
				http.Error(w, "starting up", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		dir := startUnixHTTPServer(t, mux)
		client := cachemanclient.NewClient(dir, time.Second)
		t.Cleanup(func() { _ = client.Close() })

		c := &waitCmd{
			CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
			timeout:          5,
			noRetry:          false,
		}
		require.NoError(t, c.run(context.Background()))
		require.Equal(t, int32(2), attempts.Load())
	})

	t.Run("no-retry fails on first error", func(t *testing.T) {
		var attempts atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/sync/wait", func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			http.Error(w, "starting up", http.StatusServiceUnavailable)
		})
		dir := startUnixHTTPServer(t, mux)
		client := cachemanclient.NewClient(dir, time.Second)
		t.Cleanup(func() { _ = client.Close() })

		c := &waitCmd{
			CachemanProvider: &cmd.CachemanProvider{Cacheman: client},
			timeout:          5,
			noRetry:          true,
		}
		start := time.Now()
		err := c.run(context.Background())
		require.ErrorContains(t, err, "starting up")
		require.Equal(t, int32(1), attempts.Load())
		require.Less(t, time.Since(start), time.Second)
	})
}
