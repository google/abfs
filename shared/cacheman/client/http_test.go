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
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func startHangingHTTPSocket(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	lis, err := net.Listen("unix", filepath.Join(dir, "http.socket"))
	require.NoError(t, err)

	conns := make(chan net.Conn, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
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
		<-done
		close(conns)
		for conn := range conns {
			_ = conn.Close()
		}
	})
	return dir
}

func TestHTTPClient_CanceledContext(t *testing.T) {
	t.Run("dial with canceled context", func(t *testing.T) {
		c := NewHTTPClient(t.TempDir())
		transport, ok := c.http.Transport.(*http.Transport)
		require.True(t, ok)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		conn, err := transport.DialContext(ctx, "unix", "ignored")
		if conn != nil {
			_ = conn.Close()
		}
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("ping with pre-canceled context", func(t *testing.T) {
		c := NewHTTPClient(t.TempDir())

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := c.PingContext(ctx)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("ping canceled while waiting on hanging socket", func(t *testing.T) {
		dir := startHangingHTTPSocket(t)
		c := NewHTTPClient(dir)

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		err := c.PingContext(ctx)
		require.ErrorIs(t, err, context.Canceled)
		require.Less(t, time.Since(start), time.Second)
	})
}

func TestHTTPClient_ContextMethodsHonorDeadline(t *testing.T) {
	tests := []struct {
		name string
		call func(ctx context.Context, c *HttpClient) error
	}{
		{
			name: "PingContext",
			call: func(ctx context.Context, c *HttpClient) error {
				return c.PingContext(ctx)
			},
		},
		{
			name: "RemoteOKContext",
			call: func(ctx context.Context, c *HttpClient) error {
				return c.RemoteOKContext(ctx)
			},
		},
		{
			name: "WaitIdleContext",
			call: func(ctx context.Context, c *HttpClient) error {
				return c.WaitIdleContext(ctx, time.Second)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := startHangingHTTPSocket(t)
			c := NewHTTPClient(dir)

			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()

			start := time.Now()
			err := tt.call(ctx, c)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Less(t, time.Since(start), time.Second)
		})
	}
}
