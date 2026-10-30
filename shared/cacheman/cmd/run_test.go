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
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeCacheman serves a unix socket like CacheMan: Stop closes the listener, which ends
// Serve's accept loop with an error.
type fakeCacheman struct {
	lis      net.Listener
	serveErr error
	stopErr  error
}

func (f *fakeCacheman) Serve(ctx context.Context) error {
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

func (f *fakeCacheman) Stop() error {
	_ = f.lis.Close()
	return f.stopErr
}

func TestServeUntilStopped(t *testing.T) {
	tests := []struct {
		name     string
		serveErr error
		stopErr  error
		wantErr  string
	}{
		{name: "clean stop"},
		{name: "failed stop", stopErr: errors.New("flush failed"), wantErr: "failed to stop cacheman: flush failed"},
		{name: "failed serve without a signal", serveErr: errors.New("socket gone"), wantErr: "socket gone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lis, err := net.Listen("unix", filepath.Join(t.TempDir(), "socket"))
			require.NoError(t, err)
			s := &fakeCacheman{lis: lis, serveErr: tt.serveErr, stopErr: tt.stopErr}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errC := make(chan error, 1)
			go func() {
				errC <- serveUntilStopped(ctx, s)
			}()

			if tt.serveErr == nil {
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
		})
	}
}
