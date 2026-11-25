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
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/store/localcache"
)

func fakeCachemanCmd(cacheDir string) *exec.Cmd {
	c := exec.Command(os.Args[0], "-test.run=^TestFakeCacheman$")
	c.Env = append(os.Environ(), fakeCachemanDirEnv+"="+cacheDir, fakeCachemanDetachedEnv+"=1")
	return c
}

// useFakeCacheman makes runDetached start the fake cacheman.
func useFakeCacheman(t *testing.T) {
	old := newRunCmd
	newRunCmd = func(inst *instance.Instance) (*exec.Cmd, error) { return fakeCachemanCmd(inst.Config.CacheDir), nil }
	t.Cleanup(func() { newRunCmd = old })
}

// reapOnCleanup kills and reaps pid at the end of the test. The test process is the
// parent of a detached cacheman, which init would reap in use.
func reapOnCleanup(t *testing.T, pid int) {
	t.Helper()
	process, err := os.FindProcess(pid)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = process.Kill()
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &ws, 0, nil)
	})
}

// waitForPeer waits until a cacheman listens on the socket of cacheDir.
func waitForPeer(t *testing.T, cacheDir string) client.Peer {
	t.Helper()
	var peer client.Peer
	require.Eventually(t, func() bool {
		var err error
		peer, err = client.DialPeer(cacheDir, time.Second)
		return err == nil
	}, 10*time.Second, 10*time.Millisecond)
	return peer
}

// startDetachedFakeCacheman starts the fake cacheman as restart starts cacheman and
// waits until it listens. It is not reaped before the end of the test.
func startDetachedFakeCacheman(t *testing.T, cacheDir string) int {
	t.Helper()
	pid, err := startDetached(fakeCachemanCmd(cacheDir), filepath.Join(cacheDir, "cachemand"))
	require.NoError(t, err)
	reapOnCleanup(t, pid)
	require.Equal(t, pid, waitForPeer(t, cacheDir).Pid)
	return pid
}

func TestStartDetached_OutlivesCommand(t *testing.T) {
	cacheDir := t.TempDir()
	pid := startDetachedFakeCacheman(t, cacheDir)

	time.Sleep(500 * time.Millisecond)
	peer, err := client.DialPeer(cacheDir, time.Second)
	require.NoError(t, err, "cacheman did not outlive the command")
	require.Equal(t, pid, peer.Pid)
	sid, err := unix.Getsid(pid)
	require.NoError(t, err)
	require.Equal(t, pid, sid)

	require.NoError(t, stopCachemanInst(context.Background(), testInstance(cacheDir)))
	out, err := os.ReadFile(filepath.Join(cacheDir, "cachemand"))
	require.NoError(t, err)
	require.Contains(t, string(out), "fake cacheman stdout")
}

func newTestRestartCmd(cacheDir string) *restartCmd {
	inst := testInstance(cacheDir)
	inst.Config.LocalOnly = true
	c := &cachemanCmd{ConfigProvider: &cmd.ConfigProvider{Instance: inst, Config: inst.Config}}
	r := NewRestartCmd(c, nil)
	r.CachemanProvider = &cmd.CachemanProvider{Cacheman: client.NewClient(cacheDir, time.Second)}
	r.timeout = 30 * time.Second
	return r
}

func TestRestartCmd_ReplacesCacheman(t *testing.T) {
	useFakeCacheman(t)
	cacheDir := t.TempDir()
	oldPid := startDetachedFakeCacheman(t, cacheDir)

	r := newTestRestartCmd(cacheDir)
	require.NoError(t, r.Exec(context.Background(), pflag.NewFlagSet("restart", pflag.ContinueOnError)))
	peer, err := client.DialPeer(cacheDir, time.Second)
	require.NoError(t, err)
	reapOnCleanup(t, peer.Pid)
	require.NotEqual(t, oldPid, peer.Pid)
}

func TestRestartCmd_CachemanExits(t *testing.T) {
	old := newRunCmd
	newRunCmd = func(inst *instance.Instance) (*exec.Cmd, error) {
		return exec.Command("sh", "-c", "echo socket.lock is held; exit 1"), nil
	}
	t.Cleanup(func() { newRunCmd = old })
	cacheDir := t.TempDir()

	r := newTestRestartCmd(cacheDir)
	start := time.Now()
	err := r.Exec(context.Background(), pflag.NewFlagSet("restart", pflag.ContinueOnError))
	require.ErrorIs(t, err, errCachemanExited)
	require.ErrorContains(t, err, "see "+filepath.Join(cacheDir, "cachemand"))
	require.Less(t, time.Since(start), 10*time.Second)
	out, err := os.ReadFile(filepath.Join(cacheDir, "cachemand"))
	require.NoError(t, err)
	require.Contains(t, string(out), "socket.lock is held")
}

// newTestCacheDir returns a cache directory below a temporary directory, as the clean
// of the logs removes files next to the cache directory.
func newTestCacheDir(t *testing.T) string {
	t.Helper()
	cacheDir := filepath.Join(t.TempDir(), "cache")
	require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, "tmp"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, localcache.VersionFile), []byte(localcache.CacheVersion), 0o600))
	return cacheDir
}

// Without systemd, clean --all stops cacheman for the clean and starts it again.
func TestCleanAll_StartsCachemanAgain(t *testing.T) {
	useFakeCacheman(t)
	cacheDir := newTestCacheDir(t)
	oldPid := startDetachedFakeCacheman(t, cacheDir)
	blob := filepath.Join(cacheDir, "blobs", "abc", "def")
	require.NoError(t, os.MkdirAll(filepath.Dir(blob), 0o700))
	require.NoError(t, os.WriteFile(blob, nil, 0o600))

	inst := testInstance(cacheDir)
	c := NewCleanCmd(&cachemanCmd{ConfigProvider: &cmd.ConfigProvider{Instance: inst, Config: inst.Config}})
	c.workers = 2
	c.force = true
	require.NoError(t, c.cleanAllWithoutSystemd(context.Background()))

	require.NoFileExists(t, blob)
	peer := waitForPeer(t, cacheDir)
	reapOnCleanup(t, peer.Pid)
	require.NotEqual(t, oldPid, peer.Pid)
}

func TestCleanAll_LeavesCachemanStopped(t *testing.T) {
	useFakeCacheman(t)
	cacheDir := newTestCacheDir(t)

	inst := testInstance(cacheDir)
	c := NewCleanCmd(&cachemanCmd{ConfigProvider: &cmd.ConfigProvider{Instance: inst, Config: inst.Config}})
	c.workers = 2
	c.force = true
	require.NoError(t, c.cleanAllWithoutSystemd(context.Background()))

	time.Sleep(200 * time.Millisecond)
	_, found, err := findCacheman(inst)
	require.NoError(t, err)
	require.False(t, found)
}
