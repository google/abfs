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
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/cacheman/server"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/instance"
)

type discardPrinter struct{}

func (discardPrinter) Printf(string, ...any) {}
func (discardPrinter) Println(...any)        {}
func (discardPrinter) Fatalf(format string, a ...any) {
	panic(fmt.Sprintf(format, a...))
}
func (discardPrinter) Fatal(a ...any) {
	panic(fmt.Sprint(a...))
}
func (discardPrinter) Close() error { return nil }

// fakeCacheman serves a unix socket like CacheMan: Stop closes the listener, which ends
// Serve's accept loop with an error.
type fakeCacheman struct {
	lis      net.Listener
	printer  server.Printer
	serveErr error
	drainErr error
	stopErr  error

	mu                sync.Mutex
	serveCtx          context.Context
	serveStarted      chan struct{}
	drainCalled       bool
	drainTimeout      time.Duration
	serveCtxAlive     bool
	stopBeforeDrain   bool
	stopped           bool
	serveCtxCancelled bool
}

func (f *fakeCacheman) Printer() server.Printer {
	if f.printer != nil {
		return f.printer
	}
	return discardPrinter{}
}

func (f *fakeCacheman) Serve(ctx context.Context) error {
	f.mu.Lock()
	f.serveCtx = ctx
	ch := f.serveStarted
	f.mu.Unlock()
	if ch != nil {
		close(ch)
	}
	if f.serveErr != nil {
		return f.serveErr
	}
	for {
		conn, err := f.lis.Accept()
		if err != nil {
			return err
		}
		_ = conn.Close()
	}
}

func (f *fakeCacheman) Drain(timeout time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drainCalled = true
	f.drainTimeout = timeout
	f.stopBeforeDrain = f.stopped
	if f.serveCtx != nil {
		f.serveCtxAlive = f.serveCtx.Err() == nil
	}
	return f.drainErr
}

func (f *fakeCacheman) Stop() error {
	f.mu.Lock()
	f.stopped = true
	if f.serveCtx != nil {
		f.serveCtxCancelled = f.serveCtx.Err() != nil
	}
	f.mu.Unlock()
	_ = f.lis.Close()
	return f.stopErr
}

func TestServeUntilStopped(t *testing.T) {
	tests := []struct {
		name         string
		drainTimeout time.Duration
		serveErr     error
		drainErr     error
		stopErr      error
		wantErr      string
		wantDrain    bool
	}{
		{name: "clean stop with drain", drainTimeout: 5 * time.Second, wantDrain: true},
		{name: "clean stop with drain disabled", drainTimeout: 0, wantDrain: false},
		{name: "drain timeout still stops cleanly", drainTimeout: 5 * time.Second, drainErr: errors.New("timeout waiting for dagsync to be idle"), wantDrain: true},
		{name: "failed stop", drainTimeout: 5 * time.Second, stopErr: errors.New("flush failed"), wantErr: "failed to stop cacheman: flush failed", wantDrain: true},
		{name: "failed serve without a signal skips drain", drainTimeout: 5 * time.Second, serveErr: errors.New("socket gone"), wantErr: "socket gone", wantDrain: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lis, err := net.Listen("unix", filepath.Join(t.TempDir(), "socket"))
			require.NoError(t, err)
			s := &fakeCacheman{
				lis:          lis,
				serveErr:     tt.serveErr,
				drainErr:     tt.drainErr,
				stopErr:      tt.stopErr,
				serveStarted: make(chan struct{}),
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errC := make(chan error, 1)
			go func() {
				errC <- serveUntilStopped(ctx, s, tt.drainTimeout)
			}()

			if tt.serveErr == nil {
				<-s.serveStarted
				// A signal cancels the context.
				cancel()
			}
			select {
			case err := <-errC:
				if tt.wantErr == "" {
					require.NoError(t, err)
				} else {
					require.EqualError(t, err, tt.wantErr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("serveUntilStopped did not return")
			}

			s.mu.Lock()
			defer s.mu.Unlock()
			require.Equal(t, tt.wantDrain, s.drainCalled)
			if tt.wantDrain {
				require.Equal(t, tt.drainTimeout, s.drainTimeout)
				require.True(t, s.serveCtxAlive, "Serve context must stay live during Drain")
				require.False(t, s.stopBeforeDrain, "Stop must run after Drain")
			}
			if tt.serveErr == nil {
				require.True(t, s.serveCtxCancelled, "Serve context must be cancelled before Stop")
			}
		})
	}
}

func TestRunCmdDefaultDrainTimeout(t *testing.T) {
	c := NewRunCmd(&cachemanCmd{})
	fl := pflag.NewFlagSet("run", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	require.Equal(t, 8*time.Second, c.drainTimeout)
}

func TestRunCmdNegativeDrainTimeout(t *testing.T) {
	c := NewRunCmd(&cachemanCmd{})
	fl := pflag.NewFlagSet("run", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	require.NoError(t, fl.Parse([]string{"--drain-timeout", "-1s"}))
	err := c.Exec(context.Background(), fl)
	require.EqualError(t, err, "--drain-timeout must not be negative")
}

func TestRunCmdPassesDrainTimeout(t *testing.T) {
	lis, err := net.Listen("unix", filepath.Join(t.TempDir(), "socket"))
	require.NoError(t, err)
	s := &fakeCacheman{
		lis:          lis,
		serveStarted: make(chan struct{}),
	}

	c := NewRunCmd(&cachemanCmd{ConfigProvider: &cmd.ConfigProvider{}})
	c.newServer = func(*instance.Instance, server.NewARESFunc) (cachemanServer, error) {
		return s, nil
	}
	fl := pflag.NewFlagSet("run", pflag.ContinueOnError)
	c.RegisterFlags(fl)
	require.NoError(t, fl.Parse([]string{"--drain-timeout", "3s"}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errC := make(chan error, 1)
	go func() {
		errC <- c.Exec(ctx, fl)
	}()

	<-s.serveStarted
	cancel()
	select {
	case err := <-errC:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("run command did not return")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	require.True(t, s.drainCalled)
	require.Equal(t, 3*time.Second, s.drainTimeout)
}
