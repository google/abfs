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
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

func TestCachemanDisabledIndex(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	falseVal := false
	trueVal := true
	flagsConfig := &instance.ConfigOptional{
		CacheDir:    &cacheDir,
		LogDir:      &logDir,
		LocalOnly:   &trueVal,
		EnableIndex: &falseVal,
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

	handler := c.httpHandler()

	// Helper function to make requests
	makeRequest := func(method, path string, body any, resp interface{}) int {
		var reqBody io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			require.NoError(t, err)
			reqBody = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, "http://localhost"+path, reqBody)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code == http.StatusOK && resp != nil {
			err := json.NewDecoder(w.Body).Decode(resp)
			require.NoError(t, err)
		}
		return w.Code
	}

	// Test /total-size -> should return 0
	{
		var size uint64
		code := makeRequest("GET", "/total-size", nil, &size)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, uint64(0), size)
	}

	// Test /lru-size -> should return 0
	{
		var size uint64
		code := makeRequest("GET", "/lru-size", nil, &size)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, uint64(0), size)
	}

	// Test /keep-alive -> should return true
	{
		var resp bool
		key := entities.NewHash("deadbeef")
		code := makeRequest("POST", "/keep-alive", key, &resp)
		require.Equal(t, http.StatusOK, code)
		require.True(t, resp)
	}

	// Test /use-many -> should return true
	{
		var resp bool
		keys := []entities.Hash{entities.NewHash("deadbeef")}
		code := makeRequest("POST", "/use-many", keys, &resp)
		require.Equal(t, http.StatusOK, code)
		require.True(t, resp)
	}

	// Test /local-gc -> should return true
	{
		var resp bool
		code := makeRequest("POST", "/local-gc", nil, &resp)
		require.Equal(t, http.StatusOK, code)
		require.True(t, resp)
	}

	// Test /is-object-remote -> should return false (since we are LocalOnly and index is disabled, fallback to remote headers returns false because localOnlyClient returns error for Headers)
	{
		var resp bool
		key := entities.NewHash("deadbeef")
		code := makeRequest("POST", "/is-object-remote", key, &resp)
		require.Equal(t, http.StatusOK, code)
		require.False(t, resp)
	}

	// Test /stats -> should not panic and return stats with 0/empty values for index
	{
		var stats protocol.CacheManStats
		code := makeRequest("GET", "/stats", nil, &stats)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, int64(0), stats.TotalSize)
		require.Equal(t, int64(0), stats.LRUSize)
	}
}

func TestCachemanPutRefsRace(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	trueVal := true
	falseVal := false
	flagsConfig := &instance.ConfigOptional{
		CacheDir:    &cacheDir,
		LogDir:      &logDir,
		LocalOnly:   &trueVal, // Start as LocalOnly to bypass remote setup
		EnableIndex: &falseVal,
	}

	inst := instance.NewInstance("default", tempDir, tempDir, "abfs", flagsConfig, instance.DefaultFlavor)
	defer func() {
		require.NoError(t, inst.Close())
	}()

	mockRemote := inmemory.New()
	c, err := New(inst, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, c.Stop())
	}()

	// Switch to LocalOnly = false, overwrite abfsClient, and recreate dagsync
	c.config.LocalOnly = false
	c.abfsClient = mockRemote
	c.setupDAGSync(context.Background(), c.config, true)

	// Inject artificial mailbox delay to trigger the race condition
	c.getSync().MailboxDelay = 200 * time.Millisecond

	ref := entities.RefHash{
		Ref:  "refs/heads/main",
		Hash: entities.EmptyDirHash,
	}

	startTime := time.Now()
	err = c.putRefs("myserver", "myproject", []entities.RefHash{ref}, true)
	require.NoError(t, err)
	elapsed := time.Since(startTime)

	// We expect the call to block for at least the MailboxDelay (200ms)
	require.GreaterOrEqual(t, elapsed, 200*time.Millisecond)
}

type mockARIBlobClient struct {
	blobs map[string][]byte
}

func (m *mockARIBlobClient) Close() error { return nil }
func (m *mockARIBlobClient) FetchBlob(ctx context.Context, ari string) (remote.ContentReadCloser, error) {
	data, ok := m.blobs[ari]
	if !ok {
		return nil, entities.ErrEntryNotFound
	}
	return remote.NewReaderFromBytes(data), nil
}

func TestCachemanFetchARI(t *testing.T) {
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

	ariStr := "rbe:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855:11"
	ariPayload := []byte("hello world")

	mockARI := &mockARIBlobClient{
		blobs: map[string][]byte{
			ariStr: ariPayload,
		},
	}
	c.ariClient = mockARI

	// Start worker loop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.fetchObjectsWorker(ctx)

	done := make(chan error, 1)
	c.FetchARI(ariStr, defaultFetchPriority, func(err error) {
		done <- err
	})

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for FetchARI")
	}

	// Verify content on disk
	ariHash := entities.Hash(sha1.Sum([]byte(ariStr)))
	has, err := c.store.HasARI(ctx, ariHash)
	require.NoError(t, err)
	require.True(t, has)

	r, err := c.store.OpenARI(ctx, ariHash)
	require.NoError(t, err)
	defer r.Close()
	content, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, ariPayload, content)
}

func TestCacheMan_FetchCASBlob(t *testing.T) {
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

	sha256Hex := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	blobPayload := []byte("hello cas world")
	size := int64(len(blobPayload))
	ariStr := fmt.Sprintf("rbe:%s:%d", sha256Hex, size)

	mockARI := &mockARIBlobClient{
		blobs: map[string][]byte{
			ariStr: blobPayload,
		},
	}
	c.ariClient = mockARI

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.fetchObjectsWorker(ctx)

	done := make(chan error, 1)
	c.FetchCASBlob(sha256Hex, size, defaultFetchPriority, func(err error) {
		done <- err
	})

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for FetchCASBlob")
	}

	// Verify content on disk under blobs/
	has, err := c.store.HasCASBlob(ctx, sha256Hex)
	require.NoError(t, err)
	require.True(t, has)

	blobPath := filepath.Join(cacheDir, "blobs", sha256Hex[:3], sha256Hex[3:])
	content, err := os.ReadFile(blobPath)
	require.NoError(t, err)
	require.Equal(t, blobPayload, content)
}

func TestCacheMan_FetchCASBlobUDS(t *testing.T) {
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

	sha256Hex := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	blobPayload := []byte("hello cas uds world")
	size := int64(len(blobPayload))
	ariStr := fmt.Sprintf("rbe:%s:%d", sha256Hex, size)

	mockARI := &mockARIBlobClient{
		blobs: map[string][]byte{
			ariStr: blobPayload,
		},
	}
	c.ariClient = mockARI

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.fetchObjectsWorker(ctx)
	go func() {
		_ = c.Serve(ctx)
	}()

	socketPath := filepath.Join(cacheDir, "socket")
	require.Eventually(t, func() bool {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			conn.Close()
			return true
		}
		return false
	}, 2*time.Second, 20*time.Millisecond)

	require.Eventually(t, func() bool {
		c.remoteSetupLock.Lock()
		defer c.remoteSetupLock.Unlock()
		return c.appliedRemoteConfig != nil
	}, 2*time.Second, 20*time.Millisecond)

	c.ariClient = mockARI

	conn, err := net.Dial("unix", socketPath)
	require.NoError(t, err)
	defer conn.Close()

	sha256Bytes := entities.NewSHA256(sha256Hex)

	req := &protocol.FetchCASBlobReq{
		RequestID: 123,
		SHA256:    sha256Bytes,
		Size:      size,
	}
	require.NoError(t, protocol.WriteFetchCASBlobReq(conn, req))

	resp, err := protocol.ReadFetchCASBlobResp(conn)
	require.NoError(t, err)
	require.Equal(t, protocol.RequestID(123), resp.RequestID)
	require.Equal(t, protocol.ResultSuccess, resp.Result)

	// Verify content on disk under blobs/
	has, err := c.store.HasCASBlob(ctx, sha256Hex)
	require.NoError(t, err)
	require.True(t, has)

	blobPath := filepath.Join(cacheDir, "blobs", sha256Hex[:3], sha256Hex[3:])
	content, err := os.ReadFile(blobPath)
	require.NoError(t, err)
	require.Equal(t, blobPayload, content)
}

func TestCachemanOfflineStartup(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	falseVal := false
	trueVal := true
	remoteServers := []string{"localhost:59999"}
	flagsConfig := &instance.ConfigOptional{
		CacheDir:      &cacheDir,
		LogDir:        &logDir,
		LocalOnly:     &falseVal,
		EnableIndex:   &trueVal,
		RemoteServers: &remoteServers,
	}

	inst := instance.NewInstance("default", tempDir, tempDir, "abfs", flagsConfig, instance.DefaultFlavor)
	defer func() {
		require.NoError(t, inst.Close())
	}()

	// Cacheman should start up cleanly even with unreachable remote servers
	c, err := New(inst, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, c.Stop())
	}()

	handler := c.httpHandler()

	makeRequest := func(method, path string, body any, resp interface{}) int {
		var reqBody io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			require.NoError(t, err)
			reqBody = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, "http://localhost"+path, reqBody)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code == http.StatusOK && resp != nil {
			err := json.NewDecoder(w.Body).Decode(resp)
			require.NoError(t, err)
		}
		return w.Code
	}

	// /ping should succeed
	{
		var pingResp bool
		code := makeRequest("POST", "/ping", nil, &pingResp)
		require.Equal(t, http.StatusOK, code)
		require.True(t, pingResp)
	}

	// /remote-ok should return 503 because remote is unreachable
	{
		code := makeRequest("POST", "/remote-ok", nil, nil)
		require.Equal(t, http.StatusServiceUnavailable, code)
	}

	// Sync HTTP endpoints should return 503 without panicking when sync is uninitialized
	{
		code := makeRequest("GET", "/sync/dump", nil, nil)
		require.Equal(t, http.StatusServiceUnavailable, code)

		code = makeRequest("GET", "/sync/validate", nil, nil)
		require.Equal(t, http.StatusServiceUnavailable, code)

		code = makeRequest("GET", "/sync/wait", nil, nil)
		require.Equal(t, http.StatusServiceUnavailable, code)
	}

	// Verify Phony, NewMap, and putRefs do not panic when sync is uninitialized
	{
		done := make(chan error, 1)
		c.Phony(0, []entities.Hash{entities.NewHash("0123456789abcdef0123456789abcdef01234567")}, func(err error) {
			done <- err
		})
		require.NoError(t, <-done)

		c.NewMap(entities.NewHash("0123456789abcdef0123456789abcdef01234567"), entities.NewHash("1123456789abcdef0123456789abcdef01234567"), false)

		err := c.putRefs("server", "project", []entities.RefHash{{Ref: "refs/heads/main", Hash: entities.NewHash("0123456789abcdef0123456789abcdef01234567")}}, false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "sync client not initialized")
	}
}

func TestCachemanLiveReconfig(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	falseVal := false
	trueVal := true
	badServers := []string{"localhost:59999"}
	flagsConfig := &instance.ConfigOptional{
		CacheDir:      &cacheDir,
		LogDir:        &logDir,
		LocalOnly:     &falseVal,
		EnableIndex:   &trueVal,
		RemoteServers: &badServers,
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
	go func() {
		_ = c.Serve(ctx)
	}()

	// Verify initial config has bad servers
	require.Equal(t, badServers, c.config.RemoteServers)

	// Simulate config change to a new remote server
	newServers := []string{"localhost:58888"}
	flagsConfig.RemoteServers = &newServers
	inst.NewRemoteConfig(&instance.ConfigOptional{})

	// Wait for config to be applied
	require.Eventually(t, func() bool {
		return reflect.DeepEqual(c.config.RemoteServers, newServers)
	}, 10*time.Second, 50*time.Millisecond)
}

func TestCachemanEagerPush(t *testing.T) {
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

	mockRemote := inmemory.New()
	c, err := New(inst, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, c.Stop())
	}()

	c.config.LocalOnly = false
	c.abfsClient = mockRemote
	c.setupDAGSync(context.Background(), c.config, true)

	// 1. With EagerPush = false (default), NewObject should not trigger a push.
	c.config.EagerPush = false
	blobData1 := []byte("blob content 1")
	blobHash1 := objects.HashBlobBytes(blobData1)
	require.NoError(t, c.store.WriteBlob(context.Background(), blobHash1, bytes.NewReader(blobData1)))

	c.NewObject(blobHash1, protocol.TypeBlob)
	require.Equal(t, int64(0), c.pushBlobsStat.Request.Load())

	// 2. With EagerPush = true, NewObject should trigger a push.
	c.config.EagerPush = true
	blobData2 := []byte("blob content 2")
	blobHash2 := objects.HashBlobBytes(blobData2)
	require.NoError(t, c.store.WriteBlob(context.Background(), blobHash2, bytes.NewReader(blobData2)))

	c.NewObject(blobHash2, protocol.TypeBlob)
	require.Equal(t, int64(1), c.pushBlobsStat.Request.Load())

	require.Eventually(t, func() bool {
		r, err := mockRemote.GetObject(context.Background(), blobHash2)
		if err == nil {
			r.Close()
			return true
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)
}

type mockFailingABFSClient struct {
	ABFSClient
	err error
}

func (m *mockFailingABFSClient) Version(ctx context.Context) (string, error) {
	return "", m.err
}

func (m *mockFailingABFSClient) Close() error {
	return nil
}

func TestCachemanRemoteHealthCheck(t *testing.T) {
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

	errFile := filepath.Join(cacheDir, StatusDirName, RemoteErrFileName)
	credsErrFile := filepath.Join(cacheDir, StatusDirName, CredsErrFileName)

	// 1. In LocalOnly mode, no error file should be created
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(0), c.HealthStatus())
	_, err = os.Stat(errFile)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(credsErrFile)
	require.True(t, os.IsNotExist(err))

	// 2. Set LocalOnly = false with a failing remote client -> remote.err created
	var credsErr error
	c.setCheckCreds(func(ctx context.Context) error {
		return credsErr
	})

	c.config.LocalOnly = false
	c.abfsClient = &mockFailingABFSClient{err: os.ErrDeadlineExceeded}

	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())
	content, err := os.ReadFile(errFile)
	require.NoError(t, err)
	require.Contains(t, string(content), "timestamp:")
	require.Contains(t, string(content), os.ErrDeadlineExceeded.Error())

	// 3. Test creds sampling: make the creds checker fail -> creds.err created and ENOKEY stored
	credsErr = errors.New("certificate expired")
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(syscall.ENOKEY), c.HealthStatus())
	credsContent, err := os.ReadFile(credsErrFile)
	require.NoError(t, err)
	require.Contains(t, string(credsContent), "timestamp:")
	require.Contains(t, string(credsContent), "certificate expired")

	// 4. A different creds failure replaces the previous creds.err content
	credsErr = errors.New("token invalid")
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(syscall.ENOKEY), c.HealthStatus())
	credsContent, err = os.ReadFile(credsErrFile)
	require.NoError(t, err)
	require.NotContains(t, string(credsContent), "certificate expired")
	require.Contains(t, string(credsContent), "token invalid")

	// 5. Creds checker succeeds -> creds.err is deleted, status becomes ENOTCONN
	credsErr = nil
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())
	_, err = os.Stat(credsErrFile)
	require.True(t, os.IsNotExist(err))
	// remote.err should still exist since remote is still failing
	_, err = os.Stat(errFile)
	require.NoError(t, err)

	// 6. Test custom generic CheckCreds function via setCheckCreds
	customErr := errors.New("custom creds validation failed")
	c.setCheckCreds(func(ctx context.Context) error {
		return customErr
	})
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(syscall.ENOKEY), c.HealthStatus())
	credsContent, err = os.ReadFile(credsErrFile)
	require.NoError(t, err)
	require.Contains(t, string(credsContent), "custom creds validation failed")

	// 7. Test health_bypass escape hatch: presence of health_bypass file causes early exit from checkRemoteHealth and status 0
	bypassFile := filepath.Join(cacheDir, StatusDirName, HealthBypassFileName)
	require.NoError(t, os.WriteFile(bypassFile, []byte(""), 0644))
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(0), c.HealthStatus())
	_, err = os.Stat(errFile)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(credsErrFile)
	require.True(t, os.IsNotExist(err))

	// Remove health_bypass -> both remote.err and creds.err are produced again, status ENOKEY
	require.NoError(t, os.Remove(bypassFile))
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(syscall.ENOKEY), c.HealthStatus())
	_, err = os.Stat(errFile)
	require.NoError(t, err)
	_, err = os.Stat(credsErrFile)
	require.NoError(t, err)

	// 8. Switch to a healthy remote client -> both error files are deleted upon recovery, status 0
	mockRemote := inmemory.New()
	c.abfsClient = mockRemote

	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(0), c.HealthStatus())
	_, err = os.Stat(errFile)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(credsErrFile)
	require.True(t, os.IsNotExist(err))

	// 9. If error occurs again and then switched to localOnly -> error files are cleaned up, status 0
	c.abfsClient = &mockFailingABFSClient{err: os.ErrDeadlineExceeded}
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(syscall.ENOKEY), c.HealthStatus())
	_, err = os.Stat(errFile)
	require.NoError(t, err)
	_, err = os.Stat(credsErrFile)
	require.NoError(t, err)

	c.config.LocalOnly = true
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(0), c.HealthStatus())
	_, err = os.Stat(errFile)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(credsErrFile)
	require.True(t, os.IsNotExist(err))

	// 10. Test RBE empty blob checking when RBE is configured
	c.config.LocalOnly = false
	c.abfsClient = mockRemote
	c.setCheckCreds(nil)
	c.config.RBEInstance = "projects/test/instances/default"
	c.config.RBERemote = "remotebuildexecution.googleapis.com:443"

	// 10a. RBE client uninitialized -> error ENOTCONN
	c.ariClient = nil
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())
	errContent, err := os.ReadFile(errFile)
	require.NoError(t, err)
	require.Contains(t, string(errContent), "rbe: client not initialized")

	// 10b. RBE client failing on empty blob -> error ENOTCONN
	c.ariClient = &mockARIBlobClient{blobs: map[string][]byte{}}
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())
	errContent, err = os.ReadFile(errFile)
	require.NoError(t, err)
	require.Contains(t, string(errContent), "rbe:")

	// 10c. RBE client returning empty blob -> healthy, error files removed, status 0
	emptyARI := "rbe:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855:0"
	c.ariClient = &mockARIBlobClient{blobs: map[string][]byte{emptyARI: {}}}
	c.checkRemoteHealth(context.Background())
	require.Equal(t, int32(0), c.HealthStatus())
	_, err = os.Stat(errFile)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(credsErrFile)
	require.True(t, os.IsNotExist(err))
}

func TestCheckStatusProtocol(t *testing.T) {
	c := &CacheMan{
		printer: &bufferPrinter{},
	}

	conn, serverConn := net.Pipe()
	defer conn.Close()

	srvConn := newConnection(c, serverConn)
	defer srvConn.Close()
	go func() {
		_ = srvConn.Serve()
	}()

	// 1. When status is 0 (healthy)
	c.setHealthStatusCode(0, "")
	require.NoError(t, protocol.WriteCheckStatusReq(conn, 42))
	resp, err := protocol.ReadCheckStatusResp(conn)
	require.NoError(t, err)
	require.Equal(t, protocol.RequestID(42), resp.RequestID)
	require.Equal(t, protocol.ResultSuccess, resp.Result)
	require.Equal(t, int32(0), resp.ErrorCode)

	// 2. When status is ENOTCONN (remote down)
	c.setHealthStatusCode(int32(syscall.ENOTCONN), "connection refused")
	require.NoError(t, protocol.WriteCheckStatusReq(conn, 43))
	resp, err = protocol.ReadCheckStatusResp(conn)
	require.NoError(t, err)
	require.Equal(t, protocol.RequestID(43), resp.RequestID)
	require.Equal(t, protocol.ResultFailed, resp.Result)
	require.Equal(t, int32(syscall.ENOTCONN), resp.ErrorCode)

	// 3. When status is ENOKEY (creds bad)
	c.setHealthStatusCode(int32(syscall.ENOKEY), "expired creds")
	require.NoError(t, protocol.WriteCheckStatusReq(conn, 44))
	resp, err = protocol.ReadCheckStatusResp(conn)
	require.NoError(t, err)
	require.Equal(t, protocol.RequestID(44), resp.RequestID)
	require.Equal(t, protocol.ResultFailed, resp.Result)
	require.Equal(t, int32(syscall.ENOKEY), resp.ErrorCode)
}

type bufferPrinter struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *bufferPrinter) Printf(format string, a ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fmt.Fprintf(&b.buf, format, a...)
}

func (b *bufferPrinter) Println(a ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fmt.Fprintln(&b.buf, a...)
}

func (b *bufferPrinter) Fatalf(format string, a ...any) {
	b.Printf(format, a...)
}

func (b *bufferPrinter) Fatal(a ...any) {
	b.Println(a...)
}

func (b *bufferPrinter) Close() error {
	return nil
}

func TestSetHealthStatusCode(t *testing.T) {
	bp := &bufferPrinter{}
	c := &CacheMan{
		printer: bp,
	}

	require.Equal(t, int32(0), c.HealthStatus())

	// Storing a new non-zero value with message prints the transition and message
	c.setHealthStatusCode(int32(syscall.ENOTCONN), "network is down")
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())
	require.Contains(t, bp.buf.String(), fmt.Sprintf("health status changed from 0 to %d: network is down", syscall.ENOTCONN))

	// Storing the same value does not print again
	bp.buf.Reset()
	c.setHealthStatusCode(int32(syscall.ENOTCONN), "network is down")
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())
	require.Empty(t, bp.buf.String())

	// Storing another new non-zero value prints the new transition and message
	c.setHealthStatusCode(int32(syscall.ENOKEY), "credentials expired")
	require.Equal(t, int32(syscall.ENOKEY), c.HealthStatus())
	require.Contains(t, bp.buf.String(), fmt.Sprintf("health status changed from %d to %d: credentials expired", syscall.ENOTCONN, syscall.ENOKEY))

	// Storing 0 prints recovery without extra message
	bp.buf.Reset()
	c.setHealthStatusCode(0, "")
	require.Equal(t, int32(0), c.HealthStatus())
	require.Contains(t, bp.buf.String(), fmt.Sprintf("health status changed from %d to 0\n", syscall.ENOKEY))
}

func TestHandleRemoteOkUpdatesHealthStatus(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	falseVal := false
	flagsConfig := &instance.ConfigOptional{
		CacheDir:  &cacheDir,
		LogDir:    &logDir,
		LocalOnly: &falseVal,
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

	errFile := filepath.Join(cacheDir, StatusDirName, RemoteErrFileName)

	// Initially unconnected as set in NewCacheMan
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())

	// Configure healthy mock remote
	c.abfsClient = inmemory.New()

	handler := c.httpHandler()

	// 1. Calling /remote-ok directly should establish healthy status (0) and return 200 OK
	req := httptest.NewRequest("GET", "/remote-ok", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int32(0), c.HealthStatus())
	_, statErr := os.Stat(errFile)
	require.True(t, os.IsNotExist(statErr))

	// 2. Switch to failing remote -> /remote-ok returns 503 and establishes ENOTCONN
	c.setCheckCreds(nil)
	c.abfsClient = &mockFailingABFSClient{err: os.ErrDeadlineExceeded}

	req = httptest.NewRequest("GET", "/remote-ok", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())
	_, statErr = os.Stat(errFile)
	require.NoError(t, statErr)

	// 3. Switch back to healthy remote -> /remote-ok returns 200 OK, resets status to 0, deletes error file
	c.abfsClient = inmemory.New()

	req = httptest.NewRequest("GET", "/remote-ok", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int32(0), c.HealthStatus())
	_, statErr = os.Stat(errFile)
	require.True(t, os.IsNotExist(statErr))
}

func TestTriggerHealthCheck(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	falseVal := false
	flagsConfig := &instance.ConfigOptional{
		CacheDir:  &cacheDir,
		LogDir:    &logDir,
		LocalOnly: &falseVal,
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

	// Initial status is ENOTCONN
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())

	c.abfsClient = inmemory.New()
	go c.remoteHealthWorker(ctx)

	// Status should still be ENOTCONN before trigger (10s timer hasn't expired)
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())

	// Trigger immediate health check
	c.triggerHealthCheck()

	// Health status should update to 0 promptly without waiting 10s
	require.Eventually(t, func() bool {
		return c.HealthStatus() == 0
	}, 1*time.Second, 10*time.Millisecond)
}

func TestCheckRemoteHealthCanceledContextDoesNotSetENOKEY(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	falseVal := false
	flagsConfig := &instance.ConfigOptional{
		CacheDir:  &cacheDir,
		LogDir:    &logDir,
		LocalOnly: &falseVal,
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
	cancel() // Already canceled

	err = c.checkRemoteHealth(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.NotEqual(t, int32(syscall.ENOKEY), c.HealthStatus())
}

func TestCheckRemoteHealthTimeoutSetsENOTCONNNotENOKEY(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	falseVal := false
	flagsConfig := &instance.ConfigOptional{
		CacheDir:  &cacheDir,
		LogDir:    &logDir,
		LocalOnly: &falseVal,
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

	// Failing client returns DeadlineExceeded
	c.abfsClient = &mockFailingABFSClient{err: context.DeadlineExceeded}
	// Check creds that would fail with DeadlineExceeded
	c.setCheckCreds(func(ctx context.Context) error {
		return context.DeadlineExceeded
	})

	err = c.checkRemoteHealth(context.Background())
	require.Error(t, err)
	require.Equal(t, int32(syscall.ENOTCONN), c.HealthStatus())
}

func TestSingleflightDeduplicatesHealthChecks(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	falseVal := false
	flagsConfig := &instance.ConfigOptional{
		CacheDir:  &cacheDir,
		LogDir:    &logDir,
		LocalOnly: &falseVal,
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

	var callCount atomic.Int32
	mockClient := &mockCountingABFSClient{
		onVersion: func() {
			callCount.Add(1)
			time.Sleep(50 * time.Millisecond)
		},
	}
	c.abfsClient = mockClient

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.checkRemoteHealth(context.Background())
		}()
	}
	wg.Wait()

	// All 5 concurrent calls should have been deduplicated into 1 underlying check
	require.Equal(t, int32(1), callCount.Load())
}

type mockCountingABFSClient struct {
	ABFSClient
	onVersion func()
}

func (m *mockCountingABFSClient) Version(ctx context.Context) (string, error) {
	if m.onVersion != nil {
		m.onVersion()
	}
	return "1.0", nil
}

func (m *mockCountingABFSClient) Close() error {
	return nil
}

// TestSingleflightDeduplicatesInitDeeptree verifies that concurrent
// InitDeeptree requests for the same tree share a single remote fetch and
// DeepTree compile.
//
// This matters because InitDeeptree is served asynchronously: without the
// singleflight in resolveDeeptree, every caller in a client-side fan-out would
// independently run the full (potentially minutes-long) fetch + compile for the
// same tree. Note that fetchGenericObject does not go through queueFetchReq, so
// it does not benefit from the ingress deduplication used by FetchObject.
func TestSingleflightDeduplicatesInitDeeptree(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	falseVal := false
	flagsConfig := &instance.ConfigOptional{
		CacheDir:  &cacheDir,
		LogDir:    &logDir,
		LocalOnly: &falseVal,
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
	require.NotNil(t, c.maps, "maps store must be enabled for InitDeeptree")

	tree := entities.NewHash("0123456789abcdef0123456789abcdef01234567")

	// Hold the first fetch open until every caller has arrived. Without the
	// singleflight all of them reach GetObject, so the miss shows up as a
	// deterministic count rather than as a race.
	var fetchCount atomic.Int32
	release := make(chan struct{})
	c.abfsClient = &mockBlockingABFSClient{
		target: tree,
		onGetObject: func() {
			fetchCount.Add(1)
			<-release
		},
	}

	const callers = 8
	errs := make([]error, callers)

	var entered atomic.Int32
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entered.Add(1)
			_, errs[i] = c.resolveDeeptree(tree)
		}()
	}

	// Release the leader only once every caller is running inside
	// resolveDeeptree and the leader has reached the fetch.
	//
	// This is not a perfect barrier: "entered resolveDeeptree" is not the same
	// as "registered as a follower in initDeeptreeGroup.Do" - a few statements
	// still separate the two. The short sleep covers that residual window,
	// which is now bounded by a handful of instructions rather than by whether
	// the follower goroutines have been scheduled at all.
	require.Eventually(t, func() bool {
		return entered.Load() == callers && fetchCount.Load() >= 1
	}, 10*time.Second, 5*time.Millisecond, "expected all callers to reach the shared fetch")
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	require.Equal(t, int32(1), fetchCount.Load(),
		"concurrent InitDeeptree requests for the same tree must share one fetch")
	for i, err := range errs {
		require.Error(t, err, "caller %d should observe the shared fetch failure", i)
	}
}

// mockBlockingABFSClient counts and gates GetObject calls for a single target
// hash, ignoring any unrelated background traffic.
type mockBlockingABFSClient struct {
	ABFSClient
	target      entities.Hash
	onGetObject func()
	onCtx       func(context.Context)
}

func (m *mockBlockingABFSClient) GetObject(ctx context.Context, hash entities.Hash) (remote.ContentReadCloser, error) {
	if hash == m.target {
		if m.onCtx != nil {
			m.onCtx(ctx)
		}
		if m.onGetObject != nil {
			m.onGetObject()
		}
	}
	return nil, errors.New("synthetic fetch failure")
}

func (m *mockBlockingABFSClient) Close() error {
	return nil
}

// TestDeeptreeCompileUsesServerContext pins the context scoping of the shared
// DeepTree compile.
//
// Because the compile runs under a server-wide singleflight, its result is
// handed to every coalesced caller. Scoping it to a caller's connection would
// mean that whichever client happened to arrive first could, by disconnecting,
// fail the fetch for everyone else still waiting. It must equally not run on a
// detached background context, or it would outlive shutdown.
func TestDeeptreeCompileUsesServerContext(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	falseVal := false
	flagsConfig := &instance.ConfigOptional{
		CacheDir:  &cacheDir,
		LogDir:    &logDir,
		LocalOnly: &falseVal,
	}

	inst := instance.NewInstance("default", tempDir, tempDir, "abfs", flagsConfig, instance.DefaultFlavor)
	defer func() {
		require.NoError(t, inst.Close())
	}()

	c, err := New(inst, nil)
	require.NoError(t, err)

	tree := entities.NewHash("0123456789abcdef0123456789abcdef01234567")

	var gotCtx context.Context
	c.abfsClient = &mockBlockingABFSClient{
		target: tree,
		onCtx:  func(ctx context.Context) { gotCtx = ctx },
	}

	// Stand up a connection and cancel it, standing in for a client that
	// disconnects while a compile it triggered is still in flight.
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	conn := newConnection(c, serverConn)
	conn.cancel()
	require.Error(t, conn.ctx.Err(), "precondition: the connection context is canceled")

	_, err = c.resolveDeeptree(tree)
	require.Error(t, err, "expected the synthetic fetch failure")

	require.NotNil(t, gotCtx, "the compile should have reached the remote fetch")
	require.NoError(t, gotCtx.Err(),
		"compile context must stay live even though a connection was canceled")

	require.NoError(t, c.Stop())
	require.ErrorIs(t, gotCtx.Err(), context.Canceled,
		"compile context must be the server context, so shutdown still bounds it")
}

type mockBundledABFSClient struct {
	ABFSClient
	bundledReader remote.BundledReadCloser
}

func (m *mockBundledABFSClient) GetObject(ctx context.Context, hash entities.Hash) (remote.ContentReadCloser, error) {
	return m.bundledReader, nil
}

func (m *mockBundledABFSClient) Close() error {
	return nil
}

func TestFetchGenericObjectBundled(t *testing.T) {
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

	mainData := []byte("main blob content")
	mainHash := objects.HashBlobBytes(mainData)
	mainReader := io.NopCloser(bytes.NewReader(append([]byte(fmt.Sprintf("blob %d\x00", len(mainData))), mainData...)))

	var sidecarBlobs []remote.BundledObject
	for i := 0; i < 5; i++ {
		data := []byte(fmt.Sprintf("sidecar blob content %d", i))
		h := objects.HashBlobBytes(data)
		sidecarBlobs = append(sidecarBlobs, remote.BundledObject{Hash: h, Data: data})
	}

	var sidecarObjects []remote.BundledObject
	dt := &gitobjects.DeepTreeObject{
		DeepTree: entities.DeepTree{
			Entries: []*entities.DeepEntry{
				{Name: "file.txt", Hash: sidecarBlobs[0].Hash, Mode: 0644, Size: int64(len(sidecarBlobs[0].Data))},
			},
		},
	}
	require.NoError(t, dt.Encode())
	sidecarObjects = append(sidecarObjects, remote.BundledObject{Hash: dt.Hash, Data: dt.Encoded})

	fromHash := entities.NewHash("1111111111111111111111111111111111111111")
	toHash := entities.NewHash("2222222222222222222222222222222222222222")
	mapResults := []remote.BundledMapResult{{From: fromHash, To: toHash}}

	bundledReader := remote.NewBundledReader(
		remote.NewContentReader(mainReader, ""),
		sidecarBlobs,
		sidecarObjects,
		mapResults,
	)

	c.abfsClient = &mockBundledABFSClient{bundledReader: bundledReader}

	objType, _, err := c.fetchGenericObject(context.Background(), mainHash)
	require.NoError(t, err)
	require.Equal(t, "blob", objType)

	// Verify main blob
	hasMain, err := c.store.HasBlob(context.Background(), mainHash)
	require.NoError(t, err)
	require.True(t, hasMain)

	// Verify sidecar blobs
	for _, b := range sidecarBlobs {
		hasBlob, err := c.store.HasBlob(context.Background(), b.Hash)
		require.NoError(t, err)
		require.True(t, hasBlob)
		require.NotNil(t, c.index.Get(b.Hash))
	}

	// Verify sidecar deep tree object
	hasObj, err := c.store.HasObject(context.Background(), dt.Hash)
	require.NoError(t, err)
	require.True(t, hasObj)
	require.NotNil(t, c.index.Get(dt.Hash))

	// Verify map result
	mapped, err := c.maps.Get(fromHash)
	require.NoError(t, err)
	require.Contains(t, mapped, toHash)
}

func TestMaintenanceDiskPassPreservesRemoteObject(t *testing.T) {
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

	// 1. Write a blob to the on-disk cache
	blobData := []byte("test blob data for maintenance reconciliation")
	blobHash := objects.HashBlobBytes(blobData)
	require.NoError(t, c.store.WriteBlob(context.Background(), blobHash, bytes.NewReader(blobData)))

	// 2. Put an index entry with Local: false, Remote: true (simulating an evicted entry or in-flight fetch)
	_, _, _, _, err = c.index.Put(blobHash, &IndexEntry{
		EntryType:     EntryTypeBlob,
		Loose:         true,
		Local:         false,
		Remote:        true,
		Size:          uint64(len(blobData)),
		LastAliveTime: time.Now(),
	})
	require.NoError(t, err)

	// Verify before maintenance: file is on disk, index says Local=false
	hasBlob, err := c.store.HasBlob(context.Background(), blobHash)
	require.NoError(t, err)
	require.True(t, hasBlob)

	entBefore := c.index.Get(blobHash)
	require.NotNil(t, entBefore)
	require.False(t, entBefore.Local)
	require.True(t, entBefore.Remote)

	// 3. Reconcile the on-disk blob
	c.reconcileDiskObject(blobHash, protocol.TypeBlob, map[entities.Hash]struct{}{})

	// 4. Verify after maintenance:
	// - File was NOT deleted from disk!
	hasBlobAfter, err := c.store.HasBlob(context.Background(), blobHash)
	require.NoError(t, err)
	require.True(t, hasBlobAfter, "file must not be deleted by maintenance")

	// - Index entry is updated to Local=true and Remote=true
	entAfter := c.index.Get(blobHash)
	require.NotNil(t, entAfter)
	require.True(t, entAfter.Local, "entry must be restored to Local=true")
	require.True(t, entAfter.Remote, "entry must preserve Remote=true")

	// - LRU size must track the restored entry
	require.Greater(t, c.index.LRUSize(), int64(0), "restored remote entry must be tracked in LRU")
}

type hookOnGetIndex struct {
	EmptyIndex
	onGet func(hash entities.Hash)
}

func (h *hookOnGetIndex) Get(hash entities.Hash) *IndexEntry {
	if h.onGet != nil {
		h.onGet(hash)
	}
	return &IndexEntry{
		EntryType: EntryTypeBlob,
		Loose:     true,
		Local:     true,
		Remote:    true,
	}
}

func TestObjectsInDirIgnoresMissingFiles(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	// 1. Create valid blob files and a tmp* staging file in the same shard directory.
	blobData := []byte("valid blob")
	blobHash := objects.HashBlobBytes(blobData)
	shardDir := filepath.Join(cacheDir, "blobs", blobHash.String()[:3])
	require.NoError(t, os.MkdirAll(shardDir, 0755))
	validBlobPath := filepath.Join(shardDir, blobHash.String()[3:])
	require.NoError(t, os.WriteFile(validBlobPath, blobData, 0644))

	// Create both a persistent tmp* file and a transient tmp* file that gets unlinked
	// after readDirNames snapshots the shard directory but before cwalk lstats it.
	keptTmpPath := filepath.Join(shardDir, "tmp111111111")
	require.NoError(t, os.WriteFile(keptTmpPath, []byte("staging"), 0644))
	transientTmpPath := filepath.Join(shardDir, "tmp504464806")
	require.NoError(t, os.WriteFile(transientTmpPath, []byte("transient staging"), 0644))

	c := &CacheMan{
		config: &instance.Config{
			CacheDir: cacheDir,
		},
		index: &hookOnGetIndex{
			onGet: func(hash entities.Hash) {
				_ = os.Remove(transientTmpPath)
			},
		},
	}

	// 2. Scan directory - must find the valid blob, ignore tmp* files, and not fail with "Broken symlink".
	objs, err := c.maintenanceWalkDir("blobs", protocol.TypeBlob, map[entities.Hash]struct{}{blobHash: {}})
	require.NoError(t, err)
	require.Len(t, objs, 1)
	require.Contains(t, objs, blobHash)

	// 3. Scanning a missing directory where lstat returns (nil, ENOENT) must also not return "Broken symlink".
	emptyObjs, err := c.maintenanceWalkDir("aris", protocol.TypeARI, map[entities.Hash]struct{}{})
	require.NoError(t, err)
	require.Empty(t, emptyObjs)
}

type mockBundledRemote struct {
	*inmemory.InMemoryServer
	bundledBlobs   []remote.BundledObject
	bundledObjects []remote.BundledObject
}

func (m *mockBundledRemote) GetObject(ctx context.Context, hash entities.Hash) (remote.ContentReadCloser, error) {
	rc, err := m.InMemoryServer.GetObject(ctx, hash)
	if err != nil {
		return nil, err
	}
	return remote.NewBundledReader(rc, m.bundledBlobs, m.bundledObjects, nil), nil
}

func TestCachemanFetchGenericObjectBundledBatchIndex(t *testing.T) {
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

	ctx := context.Background()
	mockServer := inmemory.New()

	// Create sidecar blob
	sideBlobData := []byte("sidecar-blob-content")
	var sideBlobBuf bytes.Buffer
	require.NoError(t, objects.WriteHeader(&sideBlobBuf, "blob", int64(len(sideBlobData))))
	sideBlobBuf.Write(sideBlobData)
	sideBlobDataWithHdr := sideBlobBuf.Bytes()
	sideBlobHash := objects.HashBytes(sideBlobDataWithHdr)
	require.NoError(t, mockServer.PutObject(ctx, sideBlobHash, remote.NewReaderFromBytes(sideBlobDataWithHdr)))

	// Create sidecar tree
	var treeContent bytes.Buffer
	treeContent.WriteString(fmt.Sprintf("%o test.txt\x00", 0100644))
	treeContent.Write(sideBlobHash[:])
	treeRaw := treeContent.Bytes()

	var treeObjBuf bytes.Buffer
	require.NoError(t, objects.WriteHeader(&treeObjBuf, "tree", int64(len(treeRaw))))
	treeObjBuf.Write(treeRaw)
	sideTreeData := treeObjBuf.Bytes()
	sideTreeHash := objects.HashBytes(sideTreeData)
	require.NoError(t, mockServer.PutObject(ctx, sideTreeHash, remote.NewReaderFromBytes(sideTreeData)))

	// Create main object
	mainData := []byte("main-blob-content")
	var mainBlobBuf bytes.Buffer
	require.NoError(t, objects.WriteHeader(&mainBlobBuf, "blob", int64(len(mainData))))
	mainBlobBuf.Write(mainData)
	mainBlobData := mainBlobBuf.Bytes()
	mainHash := objects.HashBytes(mainBlobData)
	require.NoError(t, mockServer.PutObject(ctx, mainHash, remote.NewReaderFromBytes(mainBlobData)))

	bundledClient := &mockBundledRemote{
		InMemoryServer: mockServer,
		bundledBlobs: []remote.BundledObject{
			{Hash: sideBlobHash, Data: sideBlobData},
		},
		bundledObjects: []remote.BundledObject{
			{Hash: sideTreeHash, Data: sideTreeData},
		},
	}

	c.config.LocalOnly = false
	c.abfsClient = bundledClient

	// Fetch main object which returns bundled reader with sidecars
	objType, _, err := c.fetchGenericObject(ctx, mainHash)
	require.NoError(t, err)
	require.Equal(t, "blob", objType)

	// Verify sidecar blob is in store and index
	hasBlob, err := c.store.HasBlob(ctx, sideBlobHash)
	require.NoError(t, err)
	require.True(t, hasBlob)

	blobEntry := c.index.Get(sideBlobHash)
	require.NotNil(t, blobEntry)
	require.True(t, blobEntry.Remote)
	require.Equal(t, EntryTypeBlob, blobEntry.EntryType)

	// Verify sidecar tree is in store and index
	hasTree, err := c.store.HasObject(ctx, sideTreeHash)
	require.NoError(t, err)
	require.True(t, hasTree)

	treeEntry := c.index.Get(sideTreeHash)
	require.NotNil(t, treeEntry)
	require.True(t, treeEntry.Remote)
	require.Equal(t, EntryTypeTree, treeEntry.EntryType)
}

func TestPendingAddSuppressesEagerPush(t *testing.T) {
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
	c.config.LocalOnly = false

	h := hashOf("pending-add-test-blob")

	// 1. When hash is in pendingAdd, adding local object treats it as remote
	c.pendingAdd.Add(h)

	notifyRemote, err := c.addObjectToIndexWithSize(h, EntryTypeBlob, false, 4096)
	require.NoError(t, err)
	// Must NOT notify remote (eager-push is suppressed)
	require.False(t, notifyRemote)

	// BadgerDB must already reflect Remote: true
	entry := c.index.Get(h)
	require.NotNil(t, entry)
	require.True(t, entry.Remote)

	// 2. Clear pendingAdd, simulated by indexObjectsWorker finishing
	c.pendingAdd.Remove(h)

	// Subsequent local additions still see Remote: true in BadgerDB
	notifyRemote2, err := c.addObjectToIndexWithSize(h, EntryTypeBlob, false, 4096)
	require.NoError(t, err)
	require.False(t, notifyRemote2)

	// 3. A completely new object not in pendingAdd notifies remote
	hNew := hashOf("brand-new-local-blob")
	notifyRemoteNew, err := c.addObjectToIndexWithSize(hNew, EntryTypeBlob, false, 4096)
	require.NoError(t, err)
	require.True(t, notifyRemoteNew)

	entryNew := c.index.Get(hNew)
	require.NotNil(t, entryNew)
	require.False(t, entryNew.Remote)
}

type dedupMockARIBlobClient struct {
	blobs        map[string][]byte
	fetchCount   atomic.Int32
	fetchStarted chan struct{}
	fetchBlock   chan struct{}
	once         sync.Once
}

func (m *dedupMockARIBlobClient) Close() error { return nil }

func (m *dedupMockARIBlobClient) FetchBlob(ctx context.Context, ari string) (remote.ContentReadCloser, error) {
	m.fetchCount.Add(1)
	m.once.Do(func() {
		if m.fetchStarted != nil {
			close(m.fetchStarted)
		}
	})
	if m.fetchBlock != nil {
		select {
		case <-m.fetchBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	data, ok := m.blobs[ari]
	if !ok {
		return nil, entities.ErrEntryNotFound
	}
	return remote.NewReaderFromBytes(data), nil
}

func TestFetchIngressDeduplication(t *testing.T) {
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

	ariStr := "rbe:deduptest:1234"
	ariPayload := []byte("deduplicated ari content")

	mockARI := &dedupMockARIBlobClient{
		blobs: map[string][]byte{
			ariStr: ariPayload,
		},
		fetchStarted: make(chan struct{}),
		fetchBlock:   make(chan struct{}),
	}
	c.ariClient = mockARI

	const phase1Callers = 10
	const phase2Callers = 10
	const totalCallers = phase1Callers + phase2Callers

	var allCallbacksDone sync.WaitGroup
	allCallbacksDone.Add(totalCallers)

	errs := make([]error, totalCallers)

	// Phase 1: Call FetchARI while worker is NOT yet running (queued in heap)
	var phase1Done sync.WaitGroup
	for i := 0; i < phase1Callers; i++ {
		phase1Done.Add(1)
		go func(idx int) {
			defer phase1Done.Done()
			c.FetchARI(ariStr, defaultFetchPriority, func(err error) {
				errs[idx] = err
				allCallbacksDone.Done()
			})
		}(i)
	}
	phase1Done.Wait()

	// Ingress deduplication should ensure only 1 item entered the queue
	require.Equal(t, int64(1), c.fetchARIsStat.Input.Load(), "Input stat should only increment once for deduplicated requests in heap")

	// Start fetch worker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.fetchObjectsWorker(ctx)

	// Wait until the worker pops the request and begins fetching
	select {
	case <-mockARI.fetchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for worker to start fetch")
	}

	// Phase 2: Call FetchARI while the worker is actively in-flight (blocked on fetchBlock)
	var phase2Done sync.WaitGroup
	for i := phase1Callers; i < totalCallers; i++ {
		phase2Done.Add(1)
		go func(idx int) {
			defer phase2Done.Done()
			c.FetchARI(ariStr, defaultFetchPriority, func(err error) {
				errs[idx] = err
				allCallbacksDone.Done()
			})
		}(i)
	}
	phase2Done.Wait()

	// Ingress deduplication should still ensure only 1 item entered the queue
	require.Equal(t, int64(1), c.fetchARIsStat.Input.Load(), "Input stat should remain 1 when requests arrive while in-flight")

	// Now unblock the in-flight fetch
	close(mockARI.fetchBlock)

	// Wait for all callbacks across both phases to complete
	done := make(chan struct{})
	go func() {
		allCallbacksDone.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for callbacks to finish")
	}

	// Verify all callbacks succeeded without errors
	for i := 0; i < totalCallers; i++ {
		require.NoError(t, errs[i], "caller %d returned error", i)
	}

	// Assertions on stats and remote call counts
	require.Equal(t, int64(1), c.fetchARIsStat.Input.Load(), "Input should be 1")
	require.Equal(t, int64(1), c.fetchARIsStat.Output.Load(), "Output should be 1")
	require.Equal(t, int32(1), mockARI.fetchCount.Load(), "Remote FetchBlob should be called exactly once")

	// Verify content on disk
	ariHash := entities.Hash(sha1.Sum([]byte(ariStr)))
	has, err := c.store.HasARI(ctx, ariHash)
	require.NoError(t, err)
	require.True(t, has)

	r, err := c.store.OpenARI(ctx, ariHash)
	require.NoError(t, err)
	defer r.Close()
	content, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, ariPayload, content)
}

type capturingPrinter struct {
	mu   sync.Mutex
	logs []string
}

func (p *capturingPrinter) Printf(format string, v ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logs = append(p.logs, fmt.Sprintf(format, v...))
}

func (p *capturingPrinter) Println(v ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logs = append(p.logs, fmt.Sprintln(v...))
}

func (p *capturingPrinter) Fatalf(format string, v ...any) {
	p.Printf(format, v...)
}

func (p *capturingPrinter) Fatal(v ...any) {
	p.Println(v...)
}

func (p *capturingPrinter) Close() error {
	return nil
}

func (p *capturingPrinter) contains(substr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, line := range p.logs {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

func TestMaintenanceAndEvictionRace(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	logDir := filepath.Join(tempDir, "log")

	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0755))
	require.NoError(t, os.MkdirAll(logDir, 0755))

	trueVal := true
	evictThreshold := uint64(1000)
	flagsConfig := &instance.ConfigOptional{
		CacheDir:       &cacheDir,
		LogDir:         &logDir,
		LocalOnly:      &trueVal,
		EnableIndex:    &trueVal,
		EvictThreshold: &evictThreshold,
	}

	inst := instance.NewInstance("default", tempDir, tempDir, "abfs", flagsConfig, instance.DefaultFlavor)
	defer func() {
		require.NoError(t, inst.Close())
	}()

	c, err := New(inst, nil)
	require.NoError(t, err)
	origPrinter := c.printer
	defer func() {
		c.printer = origPrinter
		require.NoError(t, c.Stop())
	}()

	printer := &capturingPrinter{}
	c.printer = printer

	// 1. Verify that missing files during maintenanceDiskPass (Local=false, Remote=true)
	// do not log "failed to restore local entry" when the file was already unlinked.
	evictedHash := objects.HashBlobBytes([]byte("already-unlinked-evicted-blob"))
	_, _, _, _, err = c.index.Put(evictedHash, &IndexEntry{
		EntryType:     EntryTypeBlob,
		Loose:         true,
		Local:         false,
		Remote:        true,
		Size:          100,
		LastAliveTime: time.Now(),
	})
	require.NoError(t, err)

	c.reconcileDiskObject(evictedHash, protocol.TypeBlob, map[entities.Hash]struct{}{})
	require.False(t, printer.contains("failed to restore local entry"), "must not log error when evicted file is already missing on disk")

	// 2. Verify that LockEviction blocks background eviction while held, and triggers eviction upon unlock.
	unlock := c.index.LockEviction()

	// Add blobs exceeding the 90% background trigger (900 bytes out of 1000) while LockEviction is held.
	for idx := range 5 {
		data := []byte(fmt.Sprintf("blob-content-%d", idx))
		h := objects.HashBlobBytes(data)
		require.NoError(t, c.store.WriteBlob(context.Background(), h, bytes.NewReader(data)))
		_, _, _, _, err := c.index.Put(h, &IndexEntry{
			EntryType:     EntryTypeBlob,
			Loose:         true,
			Local:         true,
			Remote:        true,
			Size:          200,
			LastAliveTime: time.Now(),
		})
		require.NoError(t, err)
	}

	// Eviction must not have run while LockEviction is held.
	require.Equal(t, int64(0), c.index.EvictCount(), "eviction must be blocked while LockEviction is held")

	// Releasing LockEviction should automatically re-evaluate and trigger background eviction.
	unlock()
	require.Eventually(t, func() bool {
		return c.index.EvictCount() > 0
	}, 5*time.Second, 10*time.Millisecond, "eviction should trigger automatically once LockEviction is released")

	// 3. Verify that maintenanceWork removes index entries for files deleted off disk outside of cacheman,
	// while keeping index entries for files still present on disk.
	keptData := []byte("blob-kept-on-disk")
	keptHash := objects.HashBlobBytes(keptData)
	require.NoError(t, c.store.WriteBlob(context.Background(), keptHash, bytes.NewReader(keptData)))
	_, _, _, _, err = c.index.Put(keptHash, &IndexEntry{
		EntryType:     EntryTypeBlob,
		Loose:         true,
		Local:         true,
		Remote:        true,
		Size:          uint64(len(keptData)),
		LastAliveTime: time.Now(),
	})
	require.NoError(t, err)

	userDeletedData := []byte("blob-deleted-by-user-off-disk")
	userDeletedHash := objects.HashBlobBytes(userDeletedData)
	require.NoError(t, c.store.WriteBlob(context.Background(), userDeletedHash, bytes.NewReader(userDeletedData)))
	_, _, _, _, err = c.index.Put(userDeletedHash, &IndexEntry{
		EntryType:     EntryTypeBlob,
		Loose:         true,
		Local:         true,
		Remote:        true,
		Size:          uint64(len(userDeletedData)),
		LastAliveTime: time.Now(),
	})
	require.NoError(t, err)

	// Simulate user manually deleting the file from disk outside of cacheman.
	require.NoError(t, c.store.RemoveBlob(context.Background(), userDeletedHash))

	// 4. Verify that trees in objects/ (both unindexed and Remote=false) are reconciled as EntryTypeTree.
	var treeContent bytes.Buffer
	treeContent.WriteString(fmt.Sprintf("%o file.txt\x00", 0100644))
	treeContent.Write(keptHash[:])
	treeRaw := treeContent.Bytes()

	var treeObjBuf bytes.Buffer
	require.NoError(t, objects.WriteHeader(&treeObjBuf, "tree", int64(len(treeRaw))))
	treeObjBuf.Write(treeRaw)
	treeData := treeObjBuf.Bytes()
	treeHash := objects.HashBytes(treeData)
	require.NoError(t, c.store.PutTree(context.Background(), treeHash, treeData, entities.TreeMeta{Sizes: []int64{int64(len(keptData))}}))

	require.NoError(t, c.maintenanceWork())

	require.Nil(t, c.index.Get(userDeletedHash), "entry deleted off disk by user must be removed from index by maintenanceWork")
	require.NotNil(t, c.index.Get(keptHash), "entry still present on disk must be retained in index by maintenanceWork")
	treeEnt := c.index.Get(treeHash)
	require.NotNil(t, treeEnt, "unindexed tree on disk must be indexed by maintenanceWork")
	require.Equal(t, EntryTypeTree, treeEnt.EntryType, "tree in objects/ must be reconciled as EntryTypeTree")

	// Re-running maintenance when treeEnt exists with Remote=false must also preserve EntryTypeTree.
	require.NoError(t, c.maintenanceWork())
	treeEntAfter := c.index.Get(treeHash)
	require.NotNil(t, treeEntAfter)
	require.Equal(t, EntryTypeTree, treeEntAfter.EntryType, "existing tree entry must retain EntryTypeTree across maintenance")

	// 5. Verify that clean(req.Hashes) removes both the index entry and the on-disk file,
	// so subsequent maintenanceWork does not re-index the cleaned hash.
	require.NoError(t, c.clean(protocol.CleanReq{Hashes: []string{keptHash.String()}}))
	require.Nil(t, c.index.Get(keptHash), "clean must remove hash from index")
	hasKept, err := c.store.HasBlob(context.Background(), keptHash)
	require.NoError(t, err)
	require.False(t, hasKept, "clean must remove blob file from disk when EnableIndex is true")
	require.NoError(t, c.maintenanceWork())
	require.Nil(t, c.index.Get(keptHash), "cleaned hash must not be resurrected by subsequent maintenance")
}
