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

package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

func TestFetchTypeLabel(t *testing.T) {
	tests := []struct {
		objType protocol.RequestObjectType
		want    string
	}{
		{objType: protocol.TypeBlob, want: "blob"},
		{objType: protocol.TypeTree, want: "tree"},
		{objType: protocol.TypeObject, want: "object"},
		{objType: protocol.TypeARI, want: "ari"},
		{objType: protocol.TypeCASBlob, want: "cas_blob"},
		{objType: 0, want: "other"},
		{objType: 9, want: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			require.Equal(t, tt.want, fetchTypeLabel(tt.objType))
		})
	}
}

func TestFetchResultLabel(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "ok", err: nil, want: "ok"},
		{name: "not found", err: entities.ErrEntryNotFound, want: "not_found"},
		{name: "wrapped not found", err: fmt.Errorf("failed to read: %w", os.ErrNotExist), want: "not_found"},
		{name: "error", err: errors.New("connection reset"), want: "error"},
		{name: "deadline", err: context.DeadlineExceeded, want: "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, fetchResultLabel(tt.err))
		})
	}
}

func TestInFlightRPCs(t *testing.T) {
	var r inFlightRPCs
	now := time.Now()
	require.Zero(t, r.oldestAge(now))

	first := r.begin()
	second := r.begin()
	require.NotEqual(t, first, second)
	r.mu.Lock()
	r.starts[first] = now.Add(-time.Minute)
	r.starts[second] = now.Add(-time.Second)
	r.mu.Unlock()
	require.Equal(t, time.Minute, r.oldestAge(now))

	r.end(first)
	require.Equal(t, time.Second, r.oldestAge(now))
	r.end(first)
	r.end(second)
	require.Zero(t, r.oldestAge(now))
}

// blockingVersionClient blocks Version until release is closed.
type blockingVersionClient struct {
	ABFSClient
	started chan struct{}
	release chan struct{}
}

func (c *blockingVersionClient) Version(ctx context.Context) (string, error) {
	close(c.started)
	<-c.release
	return "1", nil
}

func TestTrackingClient_OpenRPC(t *testing.T) {
	var rpcs inFlightRPCs
	inner := &blockingVersionClient{started: make(chan struct{}), release: make(chan struct{})}
	c := &trackingClient{ABFSClient: inner, rpcs: &rpcs}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Version(context.Background())
	}()
	<-inner.started
	require.Positive(t, rpcs.oldestAge(time.Now().Add(time.Second)))

	close(inner.release)
	<-done
	require.Zero(t, rpcs.oldestAge(time.Now()))
}

func TestTrackServerRPCs(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		record   bool
		tracked  bool
	}{
		{name: "NoProvider", record: true},
		{name: "NotRecording", provider: "otel"},
		{name: "Recording", provider: "otel", record: true, tracked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &CacheMan{}
			inner := &localOnlyClient{}
			got := c.trackServerRPCs(&instance.Config{MetricsProvider: tt.provider, RecordClientMetrics: tt.record}, inner)
			_, tracked := got.(*trackingClient)
			require.Equal(t, tt.tracked, tracked)
			require.Same(t, inner, unwrapABFSClient(got))
		})
	}
}

func TestTrackingClient_GetObject(t *testing.T) {
	ctx := context.Background()
	server := inmemory.New()
	content := []byte("hello")
	hash := objects.HashBlobBytes(content)
	require.NoError(t, server.PutObject(ctx, hash, remote.NewBlobReaderFromBytes(content)))

	var rpcs inFlightRPCs
	c := &trackingClient{ABFSClient: server, rpcs: &rpcs}

	// The read stays open until the reader is closed.
	reader, err := c.GetObject(ctx, hash)
	require.NoError(t, err)
	require.Positive(t, rpcs.oldestAge(time.Now().Add(time.Second)))
	_, err = io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Zero(t, rpcs.oldestAge(time.Now()))

	// A failed read is not left open.
	_, err = c.GetObject(ctx, objects.HashBlobBytes([]byte("missing")))
	require.Error(t, err)
	require.Zero(t, rpcs.oldestAge(time.Now()))

	_, err = c.Headers(ctx, []remote.Hash{hash})
	require.NoError(t, err)
	require.Zero(t, rpcs.oldestAge(time.Now()))

	require.Same(t, server, unwrapABFSClient(c))
	require.Same(t, server, unwrapABFSClient(server))
}

func TestCachemanFetchMetrics(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")
	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	trueVal := true
	flagsConfig := &instance.ConfigOptional{
		CacheDir:    &cacheDir,
		LogDir:      &logDir,
		LocalOnly:   &trueVal,
		EnableIndex: &trueVal,
	}
	inst := instance.NewInstance("default", tempDir, tempDir, "abfs", flagsConfig, instance.DefaultFlavor)
	defer func() {
		require.NoError(t, inst.Close())
	}()

	c, err := New(inst, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, c.Stop())
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := inmemory.New()
	content := []byte("hello")
	hash := objects.HashBlobBytes(content)
	require.NoError(t, server.PutObject(ctx, hash, remote.NewBlobReaderFromBytes(content)))
	c.abfsClient = &trackingClient{ABFSClient: server, rpcs: &c.serverRPCs}

	queueWait, duration := newFakeMetric(), newFakeMetric()
	c.fetchQueueWaitHist = queueWait
	c.fetchDurationHist = duration
	go c.fetchObjectsWorker(ctx)

	fetch := func(h entities.Hash) error {
		done := make(chan error, 1)
		c.FetchObject(h, protocol.TypeBlob, defaultFetchPriority, func(err error) {
			done <- err
		})
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for FetchObject")
			return nil
		}
	}
	require.NoError(t, fetch(hash))
	require.Error(t, fetch(objects.HashBlobBytes([]byte("missing"))))

	require.Len(t, queueWait.get()["blob"], 2)
	durations := duration.get()
	require.Len(t, durations["blob,ok"], 1)
	require.Len(t, durations["blob,not_found"], 1)
	require.Len(t, durations, 2)
	require.Zero(t, c.serverRPCs.oldestAge(time.Now()))
}
