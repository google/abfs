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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/daemon"
	"github.com/google/abfs/shared/instance"
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
