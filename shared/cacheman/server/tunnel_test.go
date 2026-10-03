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
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TODO: make a test with SSH to local server
func TestNewConnection(t *testing.T) {
	t.Skip("test requires an external dependency")
	require := require.New(t)

	tc, err := PortForward(&DefaultPrinter{}, "abfs-server.example.com:8080", 22000)
	require.NoError(err, "shouldn't be error from new connection")
	require.NotNil(tc, "connection shouldn't be nil")

	tc.Close()
}

func TestParseTunnelServer(t *testing.T) {
	tests := []struct {
		name     string
		server   string
		wantHost string
		wantPort string
		wantErr  bool
	}{
		{name: "host and port", server: "abfs.example.com:50051", wantHost: "abfs.example.com", wantPort: "50051"},
		{name: "https prefix", server: "https://abfs.example.com:443", wantHost: "abfs.example.com", wantPort: "443"},
		{name: "user in host", server: "user@abfs.example.com:50051", wantHost: "user@abfs.example.com", wantPort: "50051"},
		{name: "ipv6", server: "[::1]:50051", wantHost: "::1", wantPort: "50051"},
		{name: "missing port", server: "abfs.example.com", wantErr: true},
		{name: "missing host", server: ":50051", wantErr: true},
		{name: "option injection", server: "-oProxyCommand=evil:50051", wantErr: true},
		{name: "named port", server: "abfs.example.com:http", wantErr: true},
		{name: "zero port", server: "abfs.example.com:0", wantErr: true},
		{name: "port out of range", server: "abfs.example.com:70000", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := parseTunnelServer(tt.server)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantHost, host)
			require.Equal(t, tt.wantPort, port)
		})
	}
}

func TestPortForwardRejectsInvalidServer(t *testing.T) {
	_, err := PortForward(&DefaultPrinter{}, "-oProxyCommand=evil:50051", 22000)
	require.ErrorContains(t, err, "invalid ssh host")
}

func TestSSHArgs(t *testing.T) {
	require.Equal(t, []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-N",
		"-L", "22000:localhost:50051",
		"--", "abfs.example.com",
	}, sshArgs("abfs.example.com", "50051", 22000))
}

func TestLastLineWriter(t *testing.T) {
	require := require.New(t)

	var passthrough bytes.Buffer
	w := &lastLineWriter{w: &passthrough}
	require.Empty(w.LastLine())

	_, err := w.Write([]byte("Warning: Permanently added 'host' to the list of known hosts.\r\n"))
	require.NoError(err)
	require.Equal("Warning: Permanently added 'host' to the list of known hosts.", w.LastLine())

	// Lines split across writes and trailing blank lines.
	_, err = w.Write([]byte("Permission denied "))
	require.NoError(err)
	_, err = w.Write([]byte("(publickey).\n\n"))
	require.NoError(err)
	require.Equal("Permission denied (publickey).", w.LastLine())

	// A final line without a newline.
	_, err = w.Write([]byte("partial line"))
	require.NoError(err)
	require.Equal("partial line", w.LastLine())

	require.Equal("Warning: Permanently added 'host' to the list of known hosts.\r\n"+
		"Permission denied (publickey).\n\npartial line", passthrough.String())
}

func TestRealCommandWaitAddsStderr(t *testing.T) {
	require := require.New(t)

	stderr := &lastLineWriter{}
	cmd := exec.Command("sh", "-c", "echo 'debug1: connecting' >&2; echo 'Host key verification failed.' >&2; exit 255")
	cmd.Stderr = stderr
	rc := &realCommand{Cmd: cmd, stderr: stderr}

	require.NoError(rc.Start())
	err := rc.Wait()
	require.ErrorContains(err, "exit status 255")
	require.ErrorContains(err, "Host key verification failed.")
	var exitErr *exec.ExitError
	require.ErrorAs(err, &exitErr)
}

type mockCommand struct {
	startFunc func() error
	waitFunc  func() error
	killFunc  func() error
}

func (m *mockCommand) Start() error {
	if m.startFunc != nil {
		return m.startFunc()
	}
	return nil
}

func (m *mockCommand) Wait() error {
	if m.waitFunc != nil {
		return m.waitFunc()
	}
	return nil
}

func (m *mockCommand) Kill() error {
	if m.killFunc != nil {
		return m.killFunc()
	}
	return nil
}

func TestReconnectionLoop(t *testing.T) {
	require := require.New(t)

	var cmdCount int
	var mu sync.Mutex
	waitChan := make(chan struct{})
	var killOnce sync.Once
	// Buffered so a signal sent before the test goroutine is receiving is not
	// dropped by the non-blocking send below.
	cmdCreated := make(chan struct{}, 1)

	cmdFactory := func() TunnelCommand {
		mu.Lock()
		cmdCount++
		mu.Unlock()

		select {
		case cmdCreated <- struct{}{}:
		default:
		}

		return &mockCommand{
			waitFunc: func() error {
				<-waitChan
				return nil
			},
			killFunc: func() error {
				killOnce.Do(func() {
					close(waitChan)
				})
				return nil
			},
		}
	}

	tun := &tunnel{
		printer:        &DefaultPrinter{},
		maintainDone:   make(chan struct{}),
		stop:           make(chan struct{}),
		reconnectDelay: time.Millisecond,
	}

	go tun.maintain(cmdFactory, "test-server")

	// Wait for first command to be created
	<-cmdCreated

	// Trigger reconnection 3 times
	for i := 0; i < 3; i++ {
		waitChan <- struct{}{}
		<-cmdCreated
	}

	tun.Close()

	mu.Lock()
	count := cmdCount
	mu.Unlock()

	require.Equal(4, count, "Command should have been called 4 times (initial + 3 reconnections)")
}

func TestReconnectBackoff(t *testing.T) {
	require := require.New(t)

	const attempts = 6
	var mu sync.Mutex
	var starts []time.Time
	done := make(chan struct{})

	cmdFactory := func() TunnelCommand {
		mu.Lock()
		starts = append(starts, time.Now())
		if len(starts) == attempts {
			close(done)
		}
		mu.Unlock()
		return &mockCommand{startFunc: func() error { return errors.New("connection refused") }}
	}

	tun := &tunnel{
		printer:        &DefaultPrinter{},
		maintainDone:   make(chan struct{}),
		stop:           make(chan struct{}),
		reconnectDelay: time.Millisecond,
	}

	go tun.maintain(cmdFactory, "test-server")
	<-done
	tun.Close()

	mu.Lock()
	defer mu.Unlock()
	// Each retry waits at least twice as long as the previous one.
	delay := time.Millisecond
	for i := 1; i < attempts; i++ {
		require.GreaterOrEqual(starts[i].Sub(starts[i-1]), delay, "gap before attempt %d", i)
		delay *= 2
	}
}

func TestCloseRaceCondition(t *testing.T) {
	require := require.New(t)

	waitChan := make(chan struct{})
	var killOnce sync.Once

	cmdFactory := func() TunnelCommand {
		return &mockCommand{
			waitFunc: func() error {
				<-waitChan
				return nil
			},
			killFunc: func() error {
				killOnce.Do(func() {
					close(waitChan)
				})
				return nil
			},
		}
	}

	tun := &tunnel{
		printer:      &DefaultPrinter{},
		maintainDone: make(chan struct{}),
		stop:         make(chan struct{}),
	}

	go tun.maintain(cmdFactory, "test-server")

	time.Sleep(10 * time.Millisecond) // Let it start

	// Call Close concurrently
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tun.Close()
		}()
	}

	wg.Wait()

	require.True(true, "Close should handle concurrent calls safely")
}
