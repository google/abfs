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
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	aresclient "github.com/google/abfs/ares/grpc/client"
	pb "github.com/google/abfs/ares/grpc/proto"
	"github.com/google/abfs/shared/ari"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	abfsserver "github.com/google/abfs/shared/remote/grpc/server"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

// newHealthServer starts a gRPC server that only exposes the health service, reporting status for
// the ARES service. It returns the address the server is listening on.
func newHealthServer(t *testing.T, status grpc_health_v1.HealthCheckResponse_ServingStatus) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := grpc.NewServer()
	hs := health.NewServer()
	hs.SetServingStatus(pb.ARES_ServiceDesc.ServiceName, status)
	grpc_health_v1.RegisterHealthServer(s, hs)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	return lis.Addr().String()
}

// downAddress returns an address that nothing is listening on.
func downAddress(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	return addr
}

func TestSetupARESClientHealthCheck(t *testing.T) {
	newARES := func(address string, authInfo instance.AuthInfo) (remote.ARESService, error) {
		return aresclient.NewARESClient(address, authInfo)
	}

	tests := []struct {
		name        string
		address     func(t *testing.T) string
		wantSupport bool
	}{
		{
			name:        "ARES down",
			address:     downAddress,
			wantSupport: false,
		},
		{
			name: "ARES not serving",
			address: func(t *testing.T) string {
				return newHealthServer(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING)
			},
			wantSupport: false,
		},
		{
			name: "ARES serving",
			address: func(t *testing.T) string {
				return newHealthServer(t, grpc_health_v1.HealthCheckResponse_SERVING)
			},
			wantSupport: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &CacheMan{printer: new(DefaultPrinter)}
			config := &instance.Config{
				RemoteServers: []string{tc.address(t)},
				AuthType:      instance.AuthTypeNone,
				DisableTLS:    true,
			}

			require.NoError(t, c.setupARESClient(config, newARES, true))
			t.Cleanup(func() { _ = c.aresClient.Close() })
			require.Equal(t, tc.wantSupport, c.aresClient.Support())
		})
	}
}

func TestSetupRemoteInitializesDAGSyncBeforeARESAndARI(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	remoteServer := inmemory.New()
	grpcServer, err := abfsserver.NewGRPCServer(&abfsserver.GRPCServerOpts{Server: remoteServer})
	require.NoError(t, err)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		require.NoError(t, remoteServer.Close())
	})

	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")
	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	disableTLS := true
	authNone := instance.AuthTypeNone
	servers := []string{lis.Addr().String()}
	rbeInstance := "projects/test/instances/default"
	rbeRemote := "remotebuildexecution.googleapis.com:443"
	flagsConfig := &instance.ConfigOptional{
		CacheDir:      &cacheDir,
		LogDir:        &logDir,
		RemoteServers: &servers,
		AuthType:      &authNone,
		DisableTLS:    &disableTLS,
		RBEInstance:   &rbeInstance,
		RBERemote:     &rbeRemote,
	}

	inst := instance.NewInstance("default", tempDir, tempDir, "abfs", flagsConfig, instance.DefaultFlavor)
	t.Cleanup(func() { require.NoError(t, inst.Close()) })

	var (
		mu             sync.Mutex
		order          []string
		gotRBEInstance string
		gotRBERemote   string
	)
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}

	aresStarted := make(chan struct{})
	releaseARES := make(chan struct{})
	releaseARESOnce := sync.OnceFunc(func() { close(releaseARES) })
	newARES := func(address string, authInfo instance.AuthInfo) (remote.ARESService, error) {
		record("ares")
		close(aresStarted)
		<-releaseARES
		return nil, errors.New("ares unavailable")
	}

	ariStarted := make(chan struct{})
	releaseARI := make(chan struct{})
	releaseARIOnce := sync.OnceFunc(func() { close(releaseARI) })
	newARI := func(ctx context.Context, instName, remoteAddr string) (ari.BlobClient, error) {
		mu.Lock()
		order = append(order, "ari")
		gotRBEInstance = instName
		gotRBERemote = remoteAddr
		mu.Unlock()
		close(ariStarted)
		<-releaseARI
		return nil, errors.New("ari unavailable")
	}

	c, err := New(inst, newARES)
	require.NoError(t, err)
	c.newARI = newARI
	t.Cleanup(func() { require.NoError(t, c.Stop()) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan error, 1)
	setupDone := make(chan struct{})
	go func() {
		defer close(setupDone)
		done <- c.setupRemote(ctx, c.config, true)
	}()
	t.Cleanup(func() {
		releaseARESOnce()
		releaseARIOnce()
		<-setupDone
	})

	select {
	case <-aresStarted:
	case <-ariStarted:
		t.Fatal("setupARI ran before setupARESClient")
	case err := <-done:
		t.Fatalf("setupRemote finished without calling setupARESClient (err=%v)", err)
	case <-time.After(10 * time.Second):
		t.Fatal("setupARESClient was not called")
	}

	ds := c.getSync()
	require.NotNil(t, ds, "dagsync must be initialized before ARES setup runs")

	aresFrom, aresTo := entities.EmptyFileHash, entities.EmptyDirHash
	ds.AddMapResult(aresFrom, aresTo)
	require.NoError(t, ds.Wait(5*time.Second))
	require.Eventually(t, func() bool {
		maps, err := remoteServer.MapResults(ctx, aresFrom)
		return err == nil && len(maps) == 1 && maps[0] == aresTo
	}, 5*time.Second, 10*time.Millisecond)

	releaseARESOnce()

	select {
	case <-ariStarted:
	case err := <-done:
		t.Fatalf("setupRemote finished without calling setupARI (err=%v)", err)
	case <-time.After(10 * time.Second):
		t.Fatal("setupARI was not called")
	}

	ds = c.getSync()
	require.NotNil(t, ds, "dagsync must remain initialized while ARI setup runs")

	ariFrom, ariTo := entities.EmptyDirHash, entities.EmptyFileHash
	ds.AddMapResult(ariFrom, ariTo)
	require.NoError(t, ds.Wait(5*time.Second))
	require.Eventually(t, func() bool {
		maps, err := remoteServer.MapResults(ctx, ariFrom)
		return err == nil && len(maps) == 1 && maps[0] == ariTo
	}, 5*time.Second, 10*time.Millisecond)

	releaseARIOnce()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("setupRemote did not finish")
	}

	mu.Lock()
	gotOrder := append([]string(nil), order...)
	instName := gotRBEInstance
	remoteAddr := gotRBERemote
	mu.Unlock()
	require.Equal(t, []string{"ares", "ari"}, gotOrder)
	require.Equal(t, rbeInstance, instName)
	require.Equal(t, rbeRemote, remoteAddr)
}
