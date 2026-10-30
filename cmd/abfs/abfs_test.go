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

package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
	abfsgrpc "github.com/google/abfs/shared/remote/grpc"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
	"github.com/google/abfs/shared/remote/servers"
	"github.com/google/abfs/shared/store/localcache"
)

// runMainEnv makes the test binary run main() instead of the tests, so that a
// test can start the real abfs command as a child process.
const runMainEnv = "ABFS_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var listeningRE = regexp.MustCompile(`server listening at \S*:(\d+) serving`)

type serverProcess struct {
	cmd     *exec.Cmd
	addr    string
	done    chan struct{}
	waitErr error

	mu  sync.Mutex
	out bytes.Buffer
}

// startServer runs `abfs server file-based` on dir in a child process and
// waits until it listens.
func startServer(t *testing.T, dir string, serverArgs ...string) *serverProcess {
	exe, err := os.Executable()
	require.NoError(t, err)

	// The flavor, and with it the server command, comes from argv[0].
	args := append([]string{"abfs", "server", "-p", "0", "--http-port", "0"}, serverArgs...)
	args = append(args, "file-based", "-l", "3", "-d", dir)
	cmd := &exec.Cmd{
		Path: exe,
		Args: args,
		Env:  append(os.Environ(), runMainEnv+"=1", "ABFS_BASE_DIR="+t.TempDir()),
	}
	r, w, err := os.Pipe()
	require.NoError(t, err)
	cmd.Stdout = w
	cmd.Stderr = w
	require.NoError(t, cmd.Start())
	require.NoError(t, w.Close())

	p := &serverProcess{cmd: cmd, done: make(chan struct{})}
	port := make(chan string, 1)
	go func() {
		// Read all output so that the child never blocks on a full pipe.
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			p.mu.Lock()
			p.out.WriteString(line)
			p.out.WriteByte('\n')
			p.mu.Unlock()
			if m := listeningRE.FindStringSubmatch(line); m != nil {
				select {
				case port <- m[1]:
				default:
				}
			}
		}
		if err := scanner.Err(); err != nil {
			_, _ = io.Copy(io.Discard, r)
		}
		_ = r.Close()
	}()
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = cmd.Process.Kill()
			<-p.done
		}
		if t.Failed() {
			p.mu.Lock()
			t.Logf("server output:\n%s", p.out.String())
			p.mu.Unlock()
		}
	})

	select {
	case pt := <-port:
		p.addr = net.JoinHostPort("localhost", pt)
	case <-p.done:
		t.Fatalf("server exited before listening: %v", p.waitErr)
	case <-time.After(30 * time.Second):
		t.Fatal("server did not listen within 30s")
	}
	return p
}

// wait returns the exit error of the server, or fails if it does not exit
// within timeout.
func (p *serverProcess) wait(t *testing.T, timeout time.Duration) error {
	select {
	case <-p.done:
		return p.waitErr
	case <-time.After(timeout):
		t.Fatalf("server did not exit within %s", timeout)
		return nil
	}
}

// requireRefusesConnections fails unless the server stops accepting new
// connections within 10s.
func (p *serverProcess) requireRefusesConnections(t *testing.T) {
	_, port, err := net.SplitHostPort(p.addr)
	require.NoError(t, err)
	addr := net.JoinHostPort("127.0.0.1", port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if errors.Is(err, syscall.ECONNREFUSED) {
			return
		}
		if err == nil {
			_ = conn.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server still accepts connections 10s after the signal")
}

// pendingPut is a PutObject stream that has sent the first half of an object.
type pendingPut struct {
	stream pb.ABFS_PutObjectClient
	key    entities.Hash
	rest   []byte
}

// startPut opens a PutObject stream on a new connection to addr and sends the
// first half of a 2 MiB blob. When it returns, the server has registered the
// stream, so a graceful stop waits for it.
func startPut(t *testing.T, addr string) *pendingPut {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
	})
	client := pb.NewABFSClient(conn)
	ctx := metadata.AppendToOutgoingContext(t.Context(), "client-version", servers.ClientMinVersion)

	content := bytes.Repeat([]byte("abfs"), 512*1024)
	key := objects.HashBlobBytes(content)
	raw, err := io.ReadAll(remote.NewBlobReaderFromBytes(content))
	require.NoError(t, err)

	stream, err := client.PutObject(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&pb.PutObjectRequest{Chunk: &pb.Chunk{
		Encoding: remote.EncodingRaw,
		Key:      abfsgrpc.HashToBytes(key),
		Data:     raw[:len(raw)/2],
	}}))
	// The server reads the frames of one connection in order, so the reply
	// means the PutObject stream above is registered.
	_, err = client.GetVersion(ctx, &pb.GetVersionRequest{})
	require.NoError(t, err)
	return &pendingPut{stream: stream, key: key, rest: raw[len(raw)/2:]}
}

func (p *pendingPut) finish() error {
	if err := p.stream.Send(&pb.PutObjectRequest{Chunk: &pb.Chunk{IsLast: true, Data: p.rest}}); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	_, err := p.stream.CloseAndRecv()
	return err
}

func tempFiles(t *testing.T, dir string) []string {
	var tmps []string
	err := filepath.WalkDir(filepath.Join(dir, "objects"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), "tmp") {
			tmps = append(tmps, path)
		}
		return err
	})
	require.NoError(t, err)
	return tmps
}

// waitForTempFile waits until the server writes an upload to a temporary file.
func waitForTempFile(t *testing.T, dir string) {
	deadline := time.Now().Add(10 * time.Second)
	for len(tempFiles(t, dir)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no temporary file in the object store after 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func requireObjectStored(t *testing.T, dir string, key entities.Hash) {
	store, err := localcache.NewLocalStore(localcache.LocalStoreOpts{Dir: filepath.Join(dir, "objects"), Levels: 3})
	require.NoError(t, err)
	require.FileExists(t, store.Path(key))
}

func TestServer_SIGTERMDrainsPendingRPC(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	p := startServer(t, dir)
	put := startPut(t, p.addr)

	require.NoError(p.cmd.Process.Signal(syscall.SIGTERM))
	p.requireRefusesConnections(t)

	require.NoError(put.finish(), "error from the pending PutObject")
	require.NoError(p.wait(t, 15*time.Second), "error from the server process")
	requireObjectStored(t, dir, put.key)
}

func TestServer_ShutdownTimeoutClosesPendingRPC(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	p := startServer(t, dir, "--shutdown-timeout", "1s")
	put := startPut(t, p.addr)
	waitForTempFile(t, dir)

	require.NoError(p.cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(p.wait(t, 15*time.Second), "error from the server process")
	require.Equal(codes.Unavailable, status.Code(put.finish()))
	require.Empty(tempFiles(t, dir), "temporary files left in the object store")
}
