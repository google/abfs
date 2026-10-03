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
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// connectionTimeout is only respected when first setting up the connection, not on reconnects
	connectionTimeout = 5 * time.Second
	// logFrequency limits the logging in case ssh repeatedly fails to connect
	logFrequency = 1 * time.Minute
	// defaultReconnectDelay is the initial delay before restarting a tunnel that exited.
	defaultReconnectDelay = time.Second
	// maxReconnectDelay caps the exponential backoff between reconnect attempts.
	maxReconnectDelay = 30 * time.Second
	// stableTunnelDuration is how long ssh must stay up before the backoff is reset.
	stableTunnelDuration = time.Minute
	// sshWaitDelay bounds how long Wait blocks on ssh's output pipes after ssh exits,
	// e.g. when a ProxyCommand child still holds stderr open.
	sshWaitDelay = 5 * time.Second
	// maxStderrLine bounds how much of an ssh stderr line is kept for logging.
	maxStderrLine = 4096
)

// TunnelCommand abstracts the execution of external commands for testing.
type TunnelCommand interface {
	Start() error
	Wait() error
	Kill() error
}

// realCommand wraps exec.Cmd to implement TunnelCommand.
type realCommand struct {
	*exec.Cmd
	stderr *lastLineWriter
}

func (c *realCommand) Kill() error {
	if c.Process != nil {
		return c.Process.Kill()
	}
	return nil
}

// Wait waits for the command to exit. On failure, the last line the command
// wrote to stderr (usually ssh's reason for failing) is added to the error.
func (c *realCommand) Wait() error {
	err := c.Cmd.Wait()
	if err != nil && c.stderr != nil {
		if line := c.stderr.LastLine(); line != "" {
			return fmt.Errorf("%w: %s", err, line)
		}
	}
	return err
}

type tunnel struct {
	printer      Printer
	cmdLock      sync.Mutex
	cmd          TunnelCommand
	maintainDone chan struct{}
	stop         chan struct{}
	logLock      sync.Mutex
	lastLog      time.Time
	// reconnectDelay is the initial reconnect delay; defaultReconnectDelay if zero.
	reconnectDelay time.Duration
}

// PortForward maintains an SSH tunnel that forwards localhost:port to
// localhost:<rport> on the remote host, where server is "<host>:<rport>" (an
// "https://" prefix is ignored). The SSH destination is the bare host name, so
// any user, identity, or proxy settings must be configured in ~/.ssh/config, and
// authentication must work without prompting.
func PortForward(p Printer, server string, port int) (*tunnel, error) {
	host, remotePort, err := parseTunnelServer(server)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(server, "https://") {
		p.Printf("tunnel: ignoring the https:// prefix of %q; forwarding over ssh to localhost:%s on %s\n", server, remotePort, host)
	}

	t := &tunnel{
		printer:      p,
		maintainDone: make(chan struct{}),
		stop:         make(chan struct{}),
	}

	go t.maintain(func() TunnelCommand {
		return newSSHCommand(host, remotePort, port)
	}, server)

	start := time.Now()

	for time.Since(start) < connectionTimeout {
		conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		// The probe only checks reachability; a Close error doesn't make the
		// tunnel unusable, and returning here would leak the maintain goroutine.
		_ = conn.Close()
		return t, nil
	}

	// Not rate limited, so it doesn't suppress the (more useful) ssh failure reason.
	t.printer.Printf("tunnel: initial connection to %q on port %d timed out after %s, continuing in background...\n", server, port, connectionTimeout)
	return t, nil
}

func (t *tunnel) Close() {
	t.cmdLock.Lock()
	select {
	case <-t.stop:
		// Already closing
		t.cmdLock.Unlock()
	default:
		close(t.stop)
		if t.cmd != nil {
			if err := t.cmd.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.printer.Printf("Failed to kill tunnel command: %v\n", err)
			}
		}
		t.cmdLock.Unlock()
	}
	t.printer.Printf("waiting on tunnel command to exit...\n")
	<-t.maintainDone
}

// maintain keeps the tunnel command running, restarting it with exponential
// backoff until the tunnel is closed.
func (t *tunnel) maintain(cmdFactory func() TunnelCommand, server string) {
	defer close(t.maintainDone)
	initialDelay := t.reconnectDelay
	if initialDelay <= 0 {
		initialDelay = defaultReconnectDelay
	}
	delay := initialDelay
	for {
		t.cmdLock.Lock()
		select {
		case <-t.stop:
			t.cmdLock.Unlock()
			return
		default:
		}
		cmd := cmdFactory()
		t.cmd = cmd
		started := time.Now()
		err := cmd.Start()
		t.cmdLock.Unlock()

		if err == nil {
			err = cmd.Wait()
		}

		select {
		case <-t.stop:
			return
		default:
		}

		// Reconnect quickly after a tunnel that stayed up for a while.
		if time.Since(started) >= stableTunnelDuration {
			delay = initialDelay
		}
		t.logRateLimitf("tunnel to server %q closed, will try to reconnect in %s. err: %v\n", server, delay, err)
		select {
		case <-time.After(delay):
		case <-t.stop:
			return
		}
		delay = min(2*delay, maxReconnectDelay)
	}
}

func (t *tunnel) logRateLimitf(format string, v ...any) {
	t.logLock.Lock()
	defer t.logLock.Unlock()
	if time.Since(t.lastLog) > logFrequency {
		t.printer.Printf(format, v...)
		t.lastLog = time.Now()
	}
}

// parseTunnelServer splits a "<host>:<port>" server address, optionally
// prefixed with "https://", into the ssh destination host and the remote port.
func parseTunnelServer(server string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(strings.TrimPrefix(server, "https://"))
	if err != nil {
		return "", "", fmt.Errorf("expected <host>:<port> as server %q: %w", server, err)
	}
	// A host starting with "-" would be parsed by ssh as an option.
	if host == "" || strings.HasPrefix(host, "-") {
		return "", "", fmt.Errorf("invalid ssh host %q in server %q", host, server)
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return "", "", fmt.Errorf("invalid port %q in server %q", port, server)
	}
	return host, port, nil
}

// sshArgs returns the ssh arguments that forward localhost:localPort to
// localhost:remotePort on host.
func sshArgs(host, remotePort string, localPort int) []string {
	return []string{
		// Never prompt; the tunnel runs in the background without a terminal.
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		// Trust unknown host keys on first use, but still reject changed keys.
		"-o", "StrictHostKeyChecking=accept-new",
		// Exit (and get restarted) if the local port can't be forwarded.
		"-o", "ExitOnForwardFailure=yes",
		// Detect dead connections within about 45 seconds.
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-N",
		"-L", fmt.Sprintf("%d:localhost:%s", localPort, remotePort),
		// End option parsing so the host is never read as an option.
		"--", host,
	}
}

func newSSHCommand(host, remotePort string, localPort int) *realCommand {
	stderr := &lastLineWriter{w: os.Stderr}
	cmd := exec.Command("ssh", sshArgs(host, remotePort, localPort)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = sshWaitDelay
	return &realCommand{Cmd: cmd, stderr: stderr}
}

// lastLineWriter passes writes through to w (if set) and remembers the last
// non-empty line written.
type lastLineWriter struct {
	w       io.Writer
	mu      sync.Mutex
	partial []byte
	last    string
}

func (l *lastLineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.partial = append(l.partial, p...)
	for {
		i := bytes.IndexByte(l.partial, '\n')
		if i < 0 {
			break
		}
		if line := trimLine(l.partial[:i]); line != "" {
			l.last = line
		}
		l.partial = l.partial[i+1:]
	}
	if len(l.partial) > maxStderrLine {
		l.partial = l.partial[len(l.partial)-maxStderrLine:]
	}
	l.mu.Unlock()
	if l.w != nil {
		// Best effort: a failing pass-through must not block or fail the command.
		_, _ = l.w.Write(p)
	}
	return len(p), nil
}

// LastLine returns the last non-empty line written so far, including a final
// line that has no trailing newline yet.
func (l *lastLineWriter) LastLine() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if line := trimLine(l.partial); line != "" {
		return line
	}
	return l.last
}

func trimLine(b []byte) string {
	if len(b) > maxStderrLine {
		b = b[len(b)-maxStderrLine:]
	}
	return strings.TrimSpace(string(b))
}
