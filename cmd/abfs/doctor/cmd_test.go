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

package doctor

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/daemon"
	"github.com/google/abfs/shared/instance"
	abfsversion "github.com/google/abfs/shared/version"
)

func startMockCachemanServer(t *testing.T, cacheDir string, handler http.Handler) {
	socketPath := filepath.Join(cacheDir, "http.socket")
	l, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})
}

// newCachemanMux serves what the preflight asks a healthy cacheman.
func newCachemanMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(true)
	})
	mux.HandleFunc("/interface-version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"implemented": abfsversion.CachemanABFSImplementedVersion,
			"required":    abfsversion.CachemanABFSRequiredVersion,
		})
	})
	return mux
}

// shortenCachemanWait makes the ping and startup waits short for the test.
func shortenCachemanWait(t *testing.T, ping, startup time.Duration) {
	oldPing, oldStartup := cachemanPingTimeout, cachemanStartupWait
	cachemanPingTimeout, cachemanStartupWait = ping, startup
	t.Cleanup(func() { cachemanPingTimeout, cachemanStartupWait = oldPing, oldStartup })
}

// captureStderr redirects os.Stderr to a file and returns a function that reads it.
func captureStderr(t *testing.T) func() string {
	f, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = f
	t.Cleanup(func() {
		os.Stderr = old
		_ = f.Close()
	})
	return func() string {
		b, err := os.ReadFile(f.Name())
		require.NoError(t, err)
		return string(b)
	}
}

// listenWithoutServing listens on the cacheman socket and serves only after delay, as
// cacheman does while it restores its index. A negative delay never serves.
func listenWithoutServing(t *testing.T, cacheDir string, delay time.Duration) {
	l, err := net.Listen("unix", filepath.Join(cacheDir, "http.socket"))
	require.NoError(t, err)
	srv := &http.Server{Handler: newCachemanMux()}
	t.Cleanup(func() {
		_ = srv.Close()
		_ = l.Close()
	})
	if delay >= 0 {
		time.AfterFunc(delay, func() { _ = srv.Serve(l) })
	}
}

func TestCheckCachemanStatus_Running(t *testing.T) {
	cacheDir := t.TempDir()
	startMockCachemanServer(t, cacheDir, newCachemanMux())

	doc := NewDoctor(&instance.Instance{Name: "test", Config: &instance.Config{CacheDir: cacheDir}})
	require.NoError(t, doc.checkCachemanStatus())
}

func TestCheckCachemanStatus_NoSocket(t *testing.T) {
	doc := NewDoctor(&instance.Instance{Name: "test", Config: &instance.Config{CacheDir: t.TempDir()}})
	err := doc.checkCachemanStatus()
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not detect a running cacheman")
	require.Contains(t, err.Error(), "does not exist, so cacheman is not running")
	require.Contains(t, err.Error(), "`abfs --instance test cacheman status`")
}

func TestCheckCachemanStatus_NoSocketDefaultInstance(t *testing.T) {
	doc := NewDoctor(&instance.Instance{Name: instance.DefaultInstanceName, Config: &instance.Config{CacheDir: t.TempDir()}})
	err := doc.checkCachemanStatus()
	require.Error(t, err)
	require.Contains(t, err.Error(), "`abfs cacheman status`")
	require.NotContains(t, err.Error(), "--instance")
}

func TestCheckCachemanStatus_Refused(t *testing.T) {
	cacheDir := t.TempDir()
	l, err := net.Listen("unix", filepath.Join(cacheDir, "http.socket"))
	require.NoError(t, err)
	// Leave the socket file behind, as a cacheman killed with SIGKILL does.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, l.Close())

	doc := NewDoctor(&instance.Instance{Name: "test", Config: &instance.Config{CacheDir: cacheDir}})
	err = doc.checkCachemanStatus()
	require.Error(t, err)
	require.Contains(t, err.Error(), "nothing listens on")
}

func TestCheckCachemanStatus_PermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can connect to any socket")
	}
	cacheDir := t.TempDir()
	startMockCachemanServer(t, cacheDir, newCachemanMux())
	require.NoError(t, os.Chmod(filepath.Join(cacheDir, "http.socket"), 0o000))

	doc := NewDoctor(&instance.Instance{Name: "test", Config: &instance.Config{CacheDir: cacheDir}})
	err := doc.checkCachemanStatus()
	require.Error(t, err)
	require.Contains(t, err.Error(), "permission denied on")
}

func TestCheckCachemanStatus_NotADirectory(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	require.NoError(t, os.WriteFile(cacheDir, nil, 0o644))

	doc := NewDoctor(&instance.Instance{Name: "test", Config: &instance.Config{CacheDir: cacheDir}})
	err := doc.checkCachemanStatus()
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a directory")
}

func TestCheckCachemanStatus_AnswersAfterStart(t *testing.T) {
	shortenCachemanWait(t, 50*time.Millisecond, 5*time.Second)
	stderr := captureStderr(t)
	cacheDir := t.TempDir()
	listenWithoutServing(t, cacheDir, 200*time.Millisecond)

	doc := NewDoctor(&instance.Instance{Name: "test", Config: &instance.Config{CacheDir: cacheDir}})
	require.NoError(t, doc.checkCachemanStatus())
	require.Equal(t, 1, strings.Count(stderr(), "Waiting for cacheman to answer on"))
}

func TestCheckCachemanStatus_NoAnswer(t *testing.T) {
	shortenCachemanWait(t, 50*time.Millisecond, 250*time.Millisecond)
	stderr := captureStderr(t)
	cacheDir := t.TempDir()
	listenWithoutServing(t, cacheDir, -1)

	doc := NewDoctor(&instance.Instance{Name: "test", Config: &instance.Config{CacheDir: cacheDir}})
	start := time.Now()
	err := doc.checkCachemanStatus()
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not answer on")
	require.Contains(t, err.Error(), "within 250ms")
	require.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond)
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, 1, strings.Count(stderr(), "Waiting for cacheman to answer on"))
}

func TestCheckCachemanStatus_ListenQueueFull(t *testing.T) {
	shortenCachemanWait(t, 50*time.Millisecond, 250*time.Millisecond)
	stderr := captureStderr(t)
	cacheDir := t.TempDir()
	socket := filepath.Join(cacheDir, "http.socket")
	// A backlog of 0 holds one connection, so the next connect fails with EAGAIN.
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Close(fd) })
	require.NoError(t, syscall.Bind(fd, &syscall.SockaddrUnix{Name: socket}))
	require.NoError(t, syscall.Listen(fd, 0))
	held, err := net.Dial("unix", socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	_, err = net.Dial("unix", socket)
	require.ErrorIs(t, err, syscall.EAGAIN)

	doc := NewDoctor(&instance.Instance{Name: "test", Config: &instance.Config{CacheDir: cacheDir}})
	start := time.Now()
	err = doc.checkCachemanStatus()
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not detect a running cacheman")
	require.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond)
	require.Equal(t, 1, strings.Count(stderr(), "Waiting for cacheman to answer on"))
}

func TestPreflightCheck_CachemanRunning(t *testing.T) {
	cacheDir := t.TempDir()
	startMockCachemanServer(t, cacheDir, newCachemanMux())

	inst := &instance.Instance{Name: "test", Config: &instance.Config{LocalOnly: true, CacheDir: cacheDir}}
	require.NoError(t, PreflightCheck(inst, CheckCASFS, CheckServer))
}

func TestCheckCachemanRemoteOK_LocalOnly(t *testing.T) {
	doc := NewDoctor(&instance.Instance{
		Config: &instance.Config{
			LocalOnly: true,
			CacheDir:  t.TempDir(),
		},
	})
	err := doc.checkCachemanRemoteOK()
	require.NoError(t, err)
}

func TestCheckCachemanRemoteOK_Healthy(t *testing.T) {
	cacheDir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("/remote-ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(true)
	})
	startMockCachemanServer(t, cacheDir, mux)

	doc := NewDoctor(&instance.Instance{
		Config: &instance.Config{
			CacheDir: cacheDir,
		},
	})
	err := doc.checkCachemanRemoteOK()
	require.NoError(t, err)
}

func TestCheckCachemanRemoteOK_RemoteConnectionFailure(t *testing.T) {
	cacheDir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("/remote-ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode("remote connection unhealthy: connection refused")
	})
	startMockCachemanServer(t, cacheDir, mux)

	doc := NewDoctor(&instance.Instance{
		Config: &instance.Config{
			CacheDir: cacheDir,
		},
	})
	err := doc.checkCachemanRemoteOK()
	require.Error(t, err)
	require.Contains(t, err.Error(), "Cacheman reported a remote connection failure")
	require.Contains(t, err.Error(), "Check network connectivity")
}

func TestCheckCachemanRemoteOK_IncludesTailLog(t *testing.T) {
	cacheDir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("/remote-ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode("remote connection unhealthy: connection refused")
	})
	startMockCachemanServer(t, cacheDir, mux)

	doc := NewDoctor(&instance.Instance{
		Config: &instance.Config{
			CacheDir: cacheDir,
		},
	})
	doc.TailLogFn = func() string {
		return "sample cacheman tail log output"
	}
	err := doc.checkCachemanRemoteOK()
	require.Error(t, err)
	require.Contains(t, err.Error(), "Please inspect the log context below:")
	require.Contains(t, err.Error(), "sample cacheman tail log output")
}

func TestPreflightCheck_DaemonSkipped(t *testing.T) {
	t.Setenv(daemon.ABFSDaemonEnvVar, "1")
	// If daemon check was not present, passing nil instance would panic.
	if err := PreflightCheck(nil); err != nil {
		t.Fatalf("expected nil error when daemon is running, got: %v", err)
	}
}

func TestPreflightCheck_WSMountDaemonSkipped(t *testing.T) {
	t.Setenv(daemon.ABFSWSMountDaemonEnvVar, "1")
	// If daemon check was not present, passing nil instance would panic.
	if err := PreflightCheck(nil); err != nil {
		t.Fatalf("expected nil error when daemon is running, got: %v", err)
	}
}

func TestDoctor_IsSkipped_GranularAndGroups(t *testing.T) {
	t.Run("SkipInterfaceVersion group alias", func(t *testing.T) {
		doc := NewDoctor(nil).Skip(CheckInterfaceVersion)
		require.True(t, doc.IsSkipped(CheckInterfaceVersion))
		require.True(t, doc.IsSkipped(CheckCachemanCompatibility))
		require.True(t, doc.IsSkipped(CheckCASFSCompatibility))
		require.False(t, doc.IsSkipped(CheckCASFSModule))
		require.False(t, doc.IsSkipped(CheckCachemanStatus))
		require.False(t, doc.IsSkipped(CheckCachemanRemote))
		require.False(t, doc.IsSkipped(CheckServer))
	})

	t.Run("SkipRemote group alias", func(t *testing.T) {
		doc := NewDoctor(nil).Skip(CheckRemote)
		require.True(t, doc.IsSkipped(CheckCachemanRemote))
		require.True(t, doc.IsSkipped(CheckServer))
		require.False(t, doc.IsSkipped(CheckCachemanStatus))
	})

	t.Run("SkipCacheman and SkipCASFS group aliases", func(t *testing.T) {
		doc := NewDoctor(nil).Skip(CheckCacheman)
		require.True(t, doc.IsSkipped(CheckCachemanStatus))
		require.True(t, doc.IsSkipped(CheckCachemanCompatibility))
		require.True(t, doc.IsSkipped(CheckCachemanRemote))
		require.False(t, doc.IsSkipped(CheckCASFSModule))

		docCASFS := NewDoctor(nil).Skip(CheckCASFS)
		require.True(t, docCASFS.IsSkipped(CheckCASFSModule))
		require.True(t, docCASFS.IsSkipped(CheckCASFSCompatibility))
		require.True(t, docCASFS.IsSkipped(CheckCASFSUpstream))
		require.False(t, docCASFS.IsSkipped(CheckCachemanStatus))

		docCASFSMod := NewDoctor(nil).Skip(CheckCASFSModule)
		require.True(t, docCASFSMod.IsSkipped(CheckCASFSModule))
		require.True(t, docCASFSMod.IsSkipped(CheckCASFSCompatibility))
		require.False(t, docCASFSMod.IsSkipped(CheckCASFSUpstream))
	})
}

func TestDoctor_Check_SkipInterfaceVersionAndOtherChecks(t *testing.T) {
	cacheDir := t.TempDir()
	mux := http.NewServeMux()
	// Serve an incompatible (0.0.1) interface version on /interface-version
	mux.HandleFunc("/interface-version", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"implemented": "0.0.1",
			"required":    "999.0.0",
		})
	})
	mux.HandleFunc("/remote-ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(true)
	})
	startMockCachemanServer(t, cacheDir, mux)

	inst := &instance.Instance{
		Config: &instance.Config{
			LocalOnly: true,
			CacheDir:  cacheDir,
			FSType:    instance.CASFSTypeKmod,
		},
	}
	// Skip CASFS module check since we don't know if it is loaded
	// Without skipping interface-version, Check fails on checkCachemanCompatibility
	docFail := NewDoctor(inst).Skip(CheckCachemanStatus, CheckCASFSModule, CheckCASFSUpstream)
	err := docFail.Check()
	require.Error(t, err)
	require.Contains(t, err.Error(), "interface version")

	// With CheckInterfaceVersion skipped, Check succeeds
	docPass := NewDoctor(inst).Skip(CheckCachemanStatus, CheckCASFSModule, CheckCASFSUpstream, CheckInterfaceVersion)
	require.NoError(t, docPass.Check())

	// PreflightCheck with variadic CheckName arguments also succeeds
	err = PreflightCheck(inst, CheckCachemanStatus, CheckCASFSModule, CheckCASFSUpstream, CheckInterfaceVersion)
	require.NoError(t, err)
}

func TestParseCheckName(t *testing.T) {
	for _, valid := range ValidCheckNames() {
		parsed, err := ParseCheckName(string(valid))
		require.NoError(t, err)
		require.Equal(t, valid, parsed)
	}

	parsed, err := ParseCheckName("  INTERFACE-VERSION ")
	require.NoError(t, err)
	require.Equal(t, CheckInterfaceVersion, parsed)

	_, err = ParseCheckName("non-existent-check")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown doctor check")
}
