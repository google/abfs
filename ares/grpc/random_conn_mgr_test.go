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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestRandomConnMgr_ContextCancellationOnClose(t *testing.T) {
	mockRemote := &MockBlockingRemoteCloser{
		blockCh:  make(chan struct{}),
		cancelCh: make(chan struct{}),
	}

	// set a long refresh interval to ensure the background routine runs once before waiting for the interval
	connMgr := NewRandomConnMgr(context.Background(), mockRemote)

	// Wait for background routine to hit GetRefs (via hashpath.Resolve)
	<-mockRemote.blockCh

	eg := errgroup.Group{}
	eg.Go(func() error {
		return connMgr.Close()
	})

	// Verify that the context was cancelled
	<-mockRemote.cancelCh

	if err := eg.Wait(); err != nil {
		t.Fatal("Error waiting for Close():", err)
	}
}

func TestRandomConnMgr_ContextCancellationOnCancel(t *testing.T) {
	mockRemote := &MockBlockingRemoteCloser{
		blockCh:  make(chan struct{}),
		cancelCh: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	// set a long refresh interval to ensure the background routine runs once before waiting for the interval
	connMgr := NewRandomConnMgr(ctx, mockRemote)
	t.Cleanup(func() {
		require.NoError(t, connMgr.Close())
	})

	// Wait for background routine to hit GetRefs (via hashpath.Resolve)
	<-mockRemote.blockCh

	cancel()

	// Verify that the context was cancelled
	<-mockRemote.cancelCh
}

func TestRandomConnMgr_FiltersDisabledServers(t *testing.T) {
	config := ARESServerConfig{
		Servers: []ARESServer{
			{Address: "disabled-server:1234", Enabled: false, CPUCount: 1, MemorySize: 1, DiskSize: 1, CacheSize: 1},
			{Address: "enabled-server:1234", Enabled: true, CPUCount: 1, MemorySize: 1, DiskSize: 1, CacheSize: 1},
		},
	}

	mockRemote, err := NewMockConfigRemoteCloser(config)
	require.NoError(t, err, "Failed to create mock remote closer")

	// Collect connected addresses safely
	var mu sync.Mutex
	var connected []string
	mockFactory := func(addr string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
		mu.Lock()
		connected = append(connected, addr)
		mu.Unlock()
		return grpc.NewClient("passthrough:///fake", grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	connMgr := newRandomConnMgr(t.Context(), mockRemote, mockFactory)
	t.Cleanup(func() {
		require.NoError(t, connMgr.Close(), "Failed to close RandomConnMgr")
	})

	// Wait for config to be loaded
	select {
	case <-mockRemote.configReady:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for config load")
	}

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(connected) == 1 && connected[0] == "enabled-server:1234"
	}, 3*time.Second, 10*time.Millisecond, "Expected 1 connection to enabled-server:1234")
}
