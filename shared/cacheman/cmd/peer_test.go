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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/instance"
)

const (
	fakeCachemanDirEnv      = "ABFS_TEST_FAKE_CACHEMAN_DIR"
	otherPidNamespaceDirEnv = "ABFS_TEST_OTHER_PID_NAMESPACE_DIR"
	// A detached cacheman gets /dev/null as stdin, so it must not exit on its end.
	fakeCachemanDetachedEnv = "ABFS_TEST_FAKE_CACHEMAN_DETACHED"
	fakeCachemanNoSIGINTEnv = "ABFS_TEST_FAKE_CACHEMAN_NO_SIGINT"
	mainThreadExitsDirEnv   = "ABFS_TEST_MAIN_THREAD_EXITS_DIR"
)

// init is a helper process when mainThreadExitsDirEnv is set: it listens on the
// cacheman socket and ends only its main thread, so the process looks like a zombie
// while its other threads still hold the socket. Init runs on the main thread.
func init() {
	cacheDir := os.Getenv(mainThreadExitsDirEnv)
	if cacheDir == "" {
		return
	}
	signal.Ignore(syscall.SIGINT)
	if _, err := net.Listen("unix", client.HTTPSocketPath(cacheDir)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_, _, _ = syscall.RawSyscall(syscall.SYS_EXIT, 0, 0, 0)
}

func testInstance(cacheDir string) *instance.Instance {
	return &instance.Instance{Name: "test", Config: &instance.Config{CacheDir: cacheDir, LogDir: cacheDir}}
}

func pingMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("true"))
	})
	return mux
}

// serveCacheman serves handler on the cacheman socket of cacheDir in the test process.
func serveCacheman(t *testing.T, cacheDir string, handler http.Handler) {
	t.Helper()
	l, err := net.Listen("unix", client.HTTPSocketPath(cacheDir))
	require.NoError(t, err)
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// TestFakeCacheman is a helper process, not a test: startFakeCacheman runs the test
// binary with it as a cacheman that listens on http.socket, answers /ping and exits on
// SIGINT or when its stdin closes, unless the environment turns either off.
func TestFakeCacheman(t *testing.T) {
	cacheDir := os.Getenv(fakeCachemanDirEnv)
	if cacheDir == "" {
		t.Skip("helper process for the cacheman tests")
	}
	sig := make(chan os.Signal, 1)
	if os.Getenv(fakeCachemanNoSIGINTEnv) == "" {
		signal.Notify(sig, syscall.SIGINT)
	} else {
		signal.Ignore(syscall.SIGINT)
	}
	l, err := net.Listen("unix", client.HTTPSocketPath(cacheDir))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	go func() { _ = http.Serve(l, pingMux()) }()
	fmt.Println("fake cacheman stdout")

	stdinClosed := make(chan struct{})
	if os.Getenv(fakeCachemanDetachedEnv) == "" {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			close(stdinClosed)
		}()
	}
	select {
	case <-sig:
	case <-stdinClosed:
	}
	_ = l.Close()
	os.Exit(0)
}

// startFakeCacheman starts TestFakeCacheman and waits until it listens. The returned
// channel closes when the process has exited.
func startFakeCacheman(t *testing.T, cacheDir string, stdout io.Writer, env ...string) <-chan struct{} {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestFakeCacheman$")
	c.Env = append(append(os.Environ(), fakeCachemanDirEnv+"="+cacheDir), env...)
	c.Stdout = stdout
	var stderr bytes.Buffer
	c.Stderr = &stderr
	stdin, err := c.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, c.Start())

	exited := make(chan struct{})
	// Reap the process at once, so that its pid is gone when it exits.
	go func() {
		_ = c.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = c.Process.Kill()
			<-exited
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := client.DialPeer(cacheDir, time.Second); err == nil {
			return exited
		}
		select {
		case <-exited:
			t.Fatalf("fake cacheman did not start: %s", stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = c.Process.Kill()
			<-exited
			t.Fatalf("fake cacheman did not listen: %s", stderr.String())
		}
	}
}

func TestFindCacheman(t *testing.T) {
	cacheDir := t.TempDir()
	serveCacheman(t, cacheDir, pingMux())

	peer, found, err := findCacheman(testInstance(cacheDir))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, os.Getpid(), peer.Pid)
}

func TestFindCacheman_NoSocket(t *testing.T) {
	_, found, err := findCacheman(testInstance(t.TempDir()))
	require.NoError(t, err)
	require.False(t, found)
}

func TestFindCacheman_Refused(t *testing.T) {
	cacheDir := t.TempDir()
	l, err := net.Listen("unix", client.HTTPSocketPath(cacheDir))
	require.NoError(t, err)
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, l.Close())

	_, found, err := findCacheman(testInstance(cacheDir))
	require.NoError(t, err)
	require.False(t, found)
}

func TestFindCacheman_ListenQueueFull(t *testing.T) {
	oldRetries, oldWait := listenQueueRetries, listenQueueWait
	listenQueueRetries, listenQueueWait = 2, time.Millisecond
	t.Cleanup(func() { listenQueueRetries, listenQueueWait = oldRetries, oldWait })

	cacheDir := t.TempDir()
	socket := client.HTTPSocketPath(cacheDir)
	// net.Listen always asks for somaxconn, so use a raw socket with a queue of one.
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Close(fd) })
	require.NoError(t, syscall.Bind(fd, &syscall.SockaddrUnix{Name: socket}))
	require.NoError(t, syscall.Listen(fd, 0))
	full := false
	for range 16 {
		conn, err := net.Dial("unix", socket)
		if errors.Is(err, syscall.EAGAIN) {
			full = true
			break
		}
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
	}
	require.True(t, full, "listen queue never filled")

	inst := testInstance(cacheDir)
	_, found, err := findCacheman(inst)
	require.ErrorIs(t, err, errListenQueueFull)
	require.True(t, found)

	var stdout, stderr bytes.Buffer
	running, err := writeStatus(&stdout, &stderr, inst)
	require.NoError(t, err)
	require.True(t, running)
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "Warning: cacheman listens on "+socket+", but its listen queue is full")

	err = stopCachemanInst(context.Background(), inst)
	require.ErrorIs(t, err, errListenQueueFull)
	require.Contains(t, err.Error(), "find it with 'ss -xlp src "+socket+"'")
}

func TestFindCacheman_PermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may connect to any socket")
	}
	cacheDir := t.TempDir()
	serveCacheman(t, cacheDir, pingMux())
	socket := client.HTTPSocketPath(cacheDir)
	require.NoError(t, os.Chmod(socket, 0))

	inst := testInstance(cacheDir)
	want := "permission denied on " + socket + ", so cacheman may run as another user"
	_, _, err := findCacheman(inst)
	require.ErrorIs(t, err, syscall.EACCES)
	require.ErrorContains(t, err, want)
	var stdout, stderr bytes.Buffer
	_, err = writeStatus(&stdout, &stderr, inst)
	require.ErrorContains(t, err, want)
	require.Empty(t, stdout.String())
	require.ErrorContains(t, stopCachemanInst(context.Background(), inst), want)
}

// A cacheman of another user that we may connect to, as with a group-writable cache
// directory.
func TestCacheman_OtherUser(t *testing.T) {
	cacheDir := t.TempDir()
	exited := startFakeCacheman(t, cacheDir, io.Discard)
	peer, err := client.DialPeer(cacheDir, time.Second)
	require.NoError(t, err)
	old := geteuid
	geteuid = func() int { return peer.UID + 1 }
	t.Cleanup(func() { geteuid = old })

	inst := testInstance(cacheDir)
	var stdout, stderr bytes.Buffer
	running, err := writeStatus(&stdout, &stderr, inst)
	require.NoError(t, err)
	require.True(t, running)
	require.Contains(t, stderr.String(), fmt.Sprintf("Warning: cacheman (pid %d) runs as uid %d, not as uid %d", peer.Pid, peer.UID, peer.UID+1))

	err = stopCachemanInst(context.Background(), inst)
	require.ErrorContains(t, err, fmt.Sprintf("runs as uid %d, so uid %d cannot stop it", peer.UID, peer.UID+1))
	select {
	case <-exited:
		t.Fatal("cacheman exited")
	default:
	}
}

func TestWriteStatus_Answers(t *testing.T) {
	cacheDir := t.TempDir()
	serveCacheman(t, cacheDir, pingMux())

	var stdout, stderr bytes.Buffer
	running, err := writeStatus(&stdout, &stderr, testInstance(cacheDir))
	require.NoError(t, err)
	require.True(t, running)
	require.Empty(t, stderr.String())

	if strings.Contains(stderr.String(), "with pid") {
		// ps does not take the options, as BusyBox ps.
		require.Empty(t, stdout.String())
		require.Contains(t, stderr.String(), "with pid "+strconv.Itoa(os.Getpid()))
		return
	}
	require.Empty(t, stderr.String())
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, []string{"USER", "PID", "ELAPSED", "CMD"}, strings.Fields(lines[0]))
	require.Equal(t, strconv.Itoa(os.Getpid()), strings.Fields(lines[1])[1])
}

// Scripts read the ps columns on stdout, so a ps that does not take the options must
// leave stdout empty.
func TestWriteStatus_PsWithoutOptions(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\necho \"ps: bad -o argument 'uname:32'\" >&2\nexit 1\n"), 0o755))
	t.Setenv("PATH", bin)
	cacheDir := t.TempDir()
	serveCacheman(t, cacheDir, pingMux())

	var stdout, stderr bytes.Buffer
	running, err := writeStatus(&stdout, &stderr, testInstance(cacheDir))
	require.NoError(t, err)
	require.True(t, running)
	require.Empty(t, stdout.String())
	require.Equal(t, fmt.Sprintf("cacheman for instance \"test\" listens on %s with pid %d\n", client.HTTPSocketPath(cacheDir), os.Getpid()), stderr.String())
}

func TestWriteStatus_NoAnswer(t *testing.T) {
	old := cachemanAnswerTimeout
	cachemanAnswerTimeout = 100 * time.Millisecond
	t.Cleanup(func() { cachemanAnswerTimeout = old })

	cacheDir := t.TempDir()
	// The listener never accepts, like a stopped or deadlocked cacheman.
	l, err := net.Listen("unix", client.HTTPSocketPath(cacheDir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	var stdout, stderr bytes.Buffer
	start := time.Now()
	running, err := writeStatus(&stdout, &stderr, testInstance(cacheDir))
	require.NoError(t, err)
	require.True(t, running)
	require.Less(t, time.Since(start), 2*time.Second)
	require.Contains(t, stderr.String(), fmt.Sprintf("Warning: cacheman (pid %d) does not answer on %s", os.Getpid(), client.HTTPSocketPath(cacheDir)))
}

func TestWriteStatus_NotRunning(t *testing.T) {
	cacheDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	running, err := writeStatus(&stdout, &stderr, testInstance(cacheDir))
	require.NoError(t, err)
	require.False(t, running)
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "nothing listens on "+client.HTTPSocketPath(cacheDir))
}

// TestCachemanFromOtherPidNamespace is a helper process, not a test: it runs status
// and stop from a pid namespace that cannot see cacheman.
func TestCachemanFromOtherPidNamespace(t *testing.T) {
	cacheDir := os.Getenv(otherPidNamespaceDirEnv)
	if cacheDir == "" {
		t.Skip("helper process for the cacheman tests")
	}
	inst := testInstance(cacheDir)
	peer, found, err := findCacheman(inst)
	fmt.Printf("found=%v pid=%d err=%v\n", found, peer.Pid, err)
	running, err := writeStatus(os.Stdout, os.Stdout, inst)
	fmt.Printf("running=%v err=%v\n", running, err)
	fmt.Printf("stop: %v\n", stopCachemanInst(context.Background(), inst))
	os.Exit(0)
}

// A cacheman outside our pid namespace, as seen from a container that does not share
// the host's, answers but its pid is not visible.
func TestCacheman_OtherPidNamespace(t *testing.T) {
	// Some hosts do not let unprivileged users create user namespaces.
	if out, err := exec.Command("unshare", "--user", "--map-current-user", "--pid", "--fork", "true").CombinedOutput(); err != nil {
		t.Skipf("cannot enter a new pid namespace: %v: %s", err, out)
	}
	cacheDir := t.TempDir()
	exited := startFakeCacheman(t, cacheDir, io.Discard)

	c := exec.Command("unshare", "--user", "--map-current-user", "--pid", "--fork", "--kill-child",
		os.Args[0], "-test.run=^TestCachemanFromOtherPidNamespace$")
	c.Env = append(os.Environ(), otherPidNamespaceDirEnv+"="+cacheDir)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	require.NoError(t, err, stderr.String())
	got := string(out)
	require.Contains(t, got, "found=true pid=0 err=<nil>")
	require.Contains(t, got, "its process is in another pid namespace")
	require.Contains(t, got, "running=true err=<nil>")
	require.NotContains(t, got, "Warning:")
	require.Contains(t, got, "stop: cacheman for instance \"test\" runs in another pid namespace")

	select {
	case <-exited:
		t.Fatal("cacheman exited")
	default:
	}
}

func TestStopCachemanInst(t *testing.T) {
	cacheDir := t.TempDir()
	exited := startFakeCacheman(t, cacheDir, io.Discard)

	require.NoError(t, stopCachemanInst(context.Background(), testInstance(cacheDir)))
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("cacheman did not exit")
	}
}

// A cacheman that ignores SIGINT gets SIGKILL, and stop returns only once it is gone.
func TestStopCachemanInst_Kill(t *testing.T) {
	old := stopTimeout
	stopTimeout = 100 * time.Millisecond
	t.Cleanup(func() { stopTimeout = old })
	cacheDir := t.TempDir()
	startFakeCacheman(t, cacheDir, io.Discard, fakeCachemanNoSIGINTEnv+"=1")

	inst := testInstance(cacheDir)
	require.NoError(t, stopCachemanInst(context.Background(), inst))
	_, found, err := findCacheman(inst)
	require.NoError(t, err)
	require.False(t, found, "stop returned before cacheman exited")
}

// A process whose main thread has exited is a zombie in /proc/<pid>/stat while its
// other threads still exit and hold the socket, so stop must wait for them as well.
func TestStopCachemanInst_MainThreadExited(t *testing.T) {
	old := stopTimeout
	stopTimeout = 100 * time.Millisecond
	t.Cleanup(func() { stopTimeout = old })
	cacheDir := t.TempDir()
	c := exec.Command(os.Args[0], "-test.run=^$")
	c.Env = append(os.Environ(), mainThreadExitsDirEnv+"="+cacheDir)
	require.NoError(t, c.Start())
	exited := make(chan struct{})
	go func() {
		_ = c.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = c.Process.Kill()
		<-exited
	})

	stat := fmt.Sprintf("/proc/%d/stat", c.Process.Pid)
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(stat)
		return err == nil && bytes.Contains(b, []byte(") Z "))
	}, 10*time.Second, 10*time.Millisecond, "main thread did not exit")
	_, found, err := findCacheman(testInstance(cacheDir))
	require.NoError(t, err)
	require.True(t, found, "the other threads do not hold the socket")

	inst := testInstance(cacheDir)
	require.NoError(t, stopCachemanInst(context.Background(), inst))
	_, found, err = findCacheman(inst)
	require.NoError(t, err)
	require.False(t, found, "stop returned before cacheman exited")
}

// A parent that does not reap cacheman, as a container whose init does not reap,
// leaves a zombie that still takes signals.
func TestStopCachemanInst_Zombie(t *testing.T) {
	cacheDir := t.TempDir()
	pid := startDetachedFakeCacheman(t, cacheDir)

	start := time.Now()
	require.NoError(t, stopCachemanInst(context.Background(), testInstance(cacheDir)))
	require.Less(t, time.Since(start), 5*time.Second)
	var ws syscall.WaitStatus
	_, err := syscall.Wait4(pid, &ws, 0, nil)
	require.NoError(t, err)
	require.True(t, ws.Exited())
}

func TestStopCachemanInst_NotRunning(t *testing.T) {
	require.NoError(t, stopCachemanInst(context.Background(), testInstance(t.TempDir())))
}

func TestTailCmd_FindsOutputOfCacheman(t *testing.T) {
	cacheDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "cacheman"), []byte("cacheman log\n"), 0o644))
	out, err := os.Create(filepath.Join(cacheDir, "cachemand"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = out.Close() })
	startFakeCacheman(t, cacheDir, out)

	inst := testInstance(cacheDir)
	c := &cachemanCmd{ConfigProvider: &cmd.ConfigProvider{Instance: inst, Config: inst.Config}}
	tail := NewTailCmd(c)
	tail.all = true

	// tail writes to os.Stdout.
	r, w, err := os.Pipe()
	require.NoError(t, err)
	oldStdout := os.Stdout
	os.Stdout = w
	execErr := tail.Exec(context.Background(), pflag.NewFlagSet("tail", pflag.ContinueOnError))
	os.Stdout = oldStdout
	require.NoError(t, w.Close())
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, execErr)
	require.Contains(t, string(got), "cacheman log")
	require.Contains(t, string(got), "fake cacheman stdout")
}
