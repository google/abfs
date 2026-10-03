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

package daemon

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"go.uber.org/multierr"

	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

const cooperativeExit byte = 1

// ForkDaemon runs the given command in daemon mode.
// It means it spawns it in a separate process group and captures its stdout and stderr and writes
// them to this process stdout and stderr.
// But as soon as the daemon process closes its stderr and stdout, this process will assume the
// daemon is successfully detached and this process exits.
// If the daemon finishes its execution (say because of some errors), before it detaches this process
// will exit with the same error.
// It also relays signals it receives to the daemon process.
func ForkDaemon(cmd *exec.Cmd) (retErr error) {
	outReader, outWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, outReader.Close()) }()
	defer func() {
		if outWriter != nil {
			retErr = entities.FirstError(retErr, outWriter.Close())
		}
	}()

	errReader, errWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, errReader.Close()) }()
	defer func() {
		if errWriter != nil {
			retErr = entities.FirstError(retErr, errWriter.Close())
		}
	}()

	// Use a separate pipe explicitly to signal healthy process detachment.
	// Instead of relying only on pipe close, which cannot properly distinguish
	// between a cooperative close and an os.Exit() teardown, we can use a 1 byte
	// payload to signal detachment. This solves the race condition where os.Exit(1)
	// kernel actions would first close the pipe, and later properly update exit status.
	// It was possible for the parent process' WNOHANG wait to read a zero exit status
	// after the close but before the daemon's actual exit status was set.
	syncReader, syncWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, syncReader.Close()) }()
	defer func() {
		if syncWriter != nil {
			retErr = entities.FirstError(retErr, syncWriter.Close())
		}
	}()

	// https://pkg.go.dev/os/exec#Cmd
	// The child process fd gets remapped.
	// It will be 3 + i where i is the index in ExtraFiles.
	// Pass the write end of the pipe to the child through env var.
	syncFd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, syncWriter)
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", ABFSSyncPipeEnvVar, syncFd))

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	cmd.Stdin = nil
	cmd.Stdout = outWriter
	cmd.Stderr = errWriter
	if err := cmd.Start(); err != nil {
		return err
	}

	if err := outWriter.Close(); err != nil {
		return err
	}
	outWriter = nil
	if err := errWriter.Close(); err != nil {
		return err
	}
	errWriter = nil
	if err := syncWriter.Close(); err != nil {
		return err
	}
	syncWriter = nil

	// Propagate signals to the daemon before the switch happens
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, os.Signal(syscall.SIGABRT), os.Signal(syscall.SIGTERM), os.Signal(syscall.SIGQUIT))
	defer close(sigChan)
	go func() {
		for s := range sigChan {
			if err := cmd.Process.Signal(s); err != nil {
				log.Println("failed to send signal to daemon: ", err)
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	pump := func(w io.Writer, r io.Reader) {
		defer wg.Done()
		if _, err := io.Copy(w, r); err != nil {
			log.Println("failed to read from daemon: ", err)
		}
	}

	go pump(os.Stdout, outReader)
	go pump(os.Stderr, errReader)

	// First wait for stdout and stderr pipes to close.
	wg.Wait()

	// Then read from sync pipe.
	// A non-empty payload signals healthy detach.
	buf := make([]byte, 1)
	n, _ := syncReader.Read(buf)
	if n == 1 && buf[0] == cooperativeExit {
		return noHangWait(cmd)
	}

	// Otherwise, if pipe simply closed, we block until kernel updates the exit code.
	return blockingWait(cmd)
}

func noHangWait(cmd *exec.Cmd) error {
	var wstatus syscall.WaitStatus
	pid, err := syscall.Wait4(cmd.Process.Pid, &wstatus, syscall.WNOHANG, nil)
	if err != nil {
		return err
	}
	if pid != 0 {
		if wstatus.Exited() && wstatus.ExitStatus() != 0 {
			return clierr.CLIErrNoMessage(wstatus.ExitStatus())
		}
		if wstatus.Signaled() {
			return clierr.CLIErrNoMessage(128 + int(wstatus.Signal()))
		}
	}
	return nil
}

func blockingWait(cmd *exec.Cmd) error {
	var wstatus syscall.WaitStatus
	pid, err := syscall.Wait4(cmd.Process.Pid, &wstatus, 0, nil)
	if err != nil {
		return err
	}
	if pid != 0 {
		if wstatus.Exited() && wstatus.ExitStatus() != 0 {
			return clierr.CLIErrNoMessage(wstatus.ExitStatus())
		}
		if wstatus.Signaled() {
			return clierr.CLIErrNoMessage(128 + int(wstatus.Signal()))
		}
	}
	return nil
}

type Daemon struct {
	mu        sync.Mutex
	stdout    *os.File
	stderr    *os.File
	log       *os.File
	outReader *os.File
	outWriter *os.File
	errReader *os.File
	errWriter *os.File
	syncFile  *os.File

	outDaemonWriter *daemonWriter
	errDaemonWriter *daemonWriter
	switched        bool
}

type daemonWriter struct {
	done chan struct{}
	out  *os.File
	log  *os.File
}

func newDaemonWriter(out, log *os.File) *daemonWriter {
	return &daemonWriter{
		done: make(chan struct{}),
		out:  out,
		log:  log,
	}
}

func (w daemonWriter) Write(buf []byte) (int, error) {
	// Ignore the error while writing to the out (either stdout or stderr)
	_, _ = w.out.Write(buf)
	return w.log.Write(buf)
}

func (w daemonWriter) Copy(reader io.Reader) {
	defer close(w.done)
	// Ignore error
	_, _ = io.Copy(w, reader)
}

// SetupDaemon sets up this process as a daemon, it will tee the stdout and stderr to the provided
// log file as well is the process original stdout and stderr.
// When Switch() is called it closes the original stdout and stderr and replaces them with log file only.
func SetupDaemon(logFileName string) (d *Daemon, retErr error) {
	closeOnErr := func(c io.Closer) {
		if retErr != nil {
			_ = c.Close()
		}
	}

	file, err := os.OpenFile(logFileName, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0640)
	if err != nil {
		return nil, err
	}
	defer closeOnErr(file)

	d = &Daemon{
		stdout: os.Stdout,
		stderr: os.Stderr,
		log:    file,
	}

	if fdStr := os.Getenv(ABFSSyncPipeEnvVar); fdStr != "" {
		var fd int
		// Must be >= 3 because 0 is stdin, 1 is stdout, 2 is stderr.
		if _, err := fmt.Sscanf(fdStr, "%d", &fd); err == nil && fd >= 3 {
			d.syncFile = os.NewFile(uintptr(fd), "abfs_sync_pipe")
		}
	}

	d.outReader, d.outWriter, err = os.Pipe()
	if err != nil {
		return nil, err
	}
	defer closeOnErr(d.outReader)
	defer closeOnErr(d.outWriter)
	d.outDaemonWriter = newDaemonWriter(d.stdout, d.log)
	go d.outDaemonWriter.Copy(d.outReader)

	d.errReader, d.errWriter, err = os.Pipe()
	if err != nil {
		return nil, err
	}
	defer closeOnErr(d.errReader)
	defer closeOnErr(d.errWriter)

	d.errDaemonWriter = newDaemonWriter(d.stderr, d.log)
	go d.errDaemonWriter.Copy(d.errReader)
	os.Stdout = d.outWriter
	os.Stderr = d.errWriter

	log.SetOutput(d.errDaemonWriter)
	return d, nil
}

// Switch closes the original stdout and stderr and replaces them with the log file.
func (d *Daemon) Switch() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.switched {
		return
	}

	os.Stdout = d.log
	os.Stderr = d.log
	log.SetOutput(d.log)

	var err error

	if d.outWriter != nil {
		multierr.AppendInto(&err, d.outWriter.Close())
		d.outWriter = nil
	}
	if d.errWriter != nil {
		multierr.AppendInto(&err, d.errWriter.Close())
		d.errWriter = nil
	}
	if d.outDaemonWriter != nil {
		if d.outDaemonWriter.done != nil {
			<-d.outDaemonWriter.done
		}
		d.outDaemonWriter = nil
	}
	if d.errDaemonWriter != nil {
		if d.errDaemonWriter.done != nil {
			<-d.errDaemonWriter.done
		}
		d.errDaemonWriter = nil
	}
	if d.outReader != nil {
		multierr.AppendInto(&err, d.outReader.Close())
		d.outReader = nil
	}
	if d.errReader != nil {
		multierr.AppendInto(&err, d.errReader.Close())
		d.errReader = nil
	}

	if d.stdout != nil {
		multierr.AppendInto(&err, d.stdout.Close())
		d.stdout = nil
	}
	if d.stderr != nil {
		multierr.AppendInto(&err, d.stderr.Close())
		d.stderr = nil
	}
	if d.syncFile != nil {
		_, writeErr := d.syncFile.Write([]byte{cooperativeExit})
		multierr.AppendInto(&err, writeErr)
		multierr.AppendInto(&err, d.syncFile.Close())
		d.syncFile = nil
	}

	multierr.AppendInto(&err, syscall.Dup2(int(d.log.Fd()), 1))
	multierr.AppendInto(&err, syscall.Dup2(int(d.log.Fd()), 2))

	d.switched = true

	if err != nil {
		log.Printf("failed to switch daemon: %s", err)
	}
}

// Close closes the daemon pipelines and log files, ensuring all remaining buffered data is flushed.
func (d *Daemon) Close() error {
	if d == nil {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	var err error
	if d.outWriter != nil {
		multierr.AppendInto(&err, d.outWriter.Close())
		d.outWriter = nil
	}
	if d.errWriter != nil {
		multierr.AppendInto(&err, d.errWriter.Close())
		d.errWriter = nil
	}
	if d.outDaemonWriter != nil {
		if d.outDaemonWriter.done != nil {
			<-d.outDaemonWriter.done
		}
		d.outDaemonWriter = nil
	}
	if d.errDaemonWriter != nil {
		if d.errDaemonWriter.done != nil {
			<-d.errDaemonWriter.done
		}
		d.errDaemonWriter = nil
	}
	if d.outReader != nil {
		multierr.AppendInto(&err, d.outReader.Close())
		d.outReader = nil
	}
	if d.errReader != nil {
		multierr.AppendInto(&err, d.errReader.Close())
		d.errReader = nil
	}
	if !d.switched {
		if d.log != nil {
			multierr.AppendInto(&err, d.log.Close())
			d.log = nil
		}
		if d.stdout != nil {
			os.Stdout = d.stdout
			d.stdout = nil
		}
		if d.stderr != nil {
			os.Stderr = d.stderr
			d.stderr = nil
			log.SetOutput(os.Stderr)
		}
		if d.syncFile != nil {
			_ = d.syncFile.Close()
			d.syncFile = nil
		}
	}
	return err
}

const (
	ABFSDaemonEnvVar        = "ABFS_DAEMON"
	ABFSWSMountDaemonEnvVar = "ABFS_WS_MOUNT_DAEMON"
	ABFSSyncPipeEnvVar      = "ABFS_SYNC_PIPE_FD"
)

// IsDaemon returns true if the current process is running as a daemon.
func IsDaemon() bool {
	return os.Getenv(ABFSDaemonEnvVar) == "1" || os.Getenv(ABFSWSMountDaemonEnvVar) == "1"
}
