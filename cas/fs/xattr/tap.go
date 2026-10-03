// Copyright 2025 Google LLC
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

package xattr

import (
	"bytes"
	"errors"
	"fmt"
	iofs "io/fs"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/fs/tapping"
)

const (
	minimumChannel    = 1024
	channelMultiplier = 256
)

func calculateChannelCapacity() int {
	capacity := runtime.NumCPU() * channelMultiplier
	if capacity < minimumChannel {
		return minimumChannel
	}
	return capacity
}

func RunTap(cmd *exec.Cmd, hasher fs.Hasher, tapPath string, pidInfo func(int), eventHandler func(t *fs.TapFSEntry)) (*fs.TapInfo, error) {
	execStart := time.Now()

	if err := syscall.Setpgid(0, 0); err != nil {
		return nil, err
	}

	fd, err := unix.Open(tapPath, unix.O_RDWR|unix.O_TMPFILE|unix.O_EXCL|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open tmp file for dir %s (%w)", tapPath, err)
	}
	defer unix.Close(fd)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Spawn reading of tap data
	var wg sync.WaitGroup
	var decodeErr error
	wg.Add(2)

	// Dynamically scale channel capacity matching production behavior
	taps := make(chan *fs.TapFSEntry, calculateChannelCapacity())
	tapReader := tapping.TapReaderCreate(fd)

	go func() {
		defer wg.Done()
		defer close(taps)

		decodeErr = tapping.TapDecode(tapReader, hasher, taps)
	}()

	go func() {
		defer wg.Done()
		defer func() {
			// Drain taps in case of panic
			for range taps {
			}
		}()

		for t := range taps {
			eventHandler(t)
		}
	}()

	var exitErr *exec.ExitError
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if pidInfo != nil {
		pidInfo(cmd.Process.Pid)
	}
	if err := cmd.Wait(); err != nil && !errors.As(err, &exitErr) {
		return nil, err
	}

	info := &fs.TapInfo{
		Pid:       cmd.Process.Pid,
		ExitCode:  cmd.ProcessState.ExitCode(),
		Rusage:    cmd.ProcessState.SysUsage().(*syscall.Rusage),
		Timestamp: execStart,
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
	}

	// Tell kernel tap driver we are finished and check ioctl result
	if err := tapReader.CmdDone(); err != nil {
		return nil, fmt.Errorf("tap reader handshake failed: %w", err)
	}

	wg.Wait()
	if decodeErr != nil {
		return nil, decodeErr
	}

	info.RunDuration = time.Since(execStart).Seconds()
	return info, nil
}

func RunTapCmd(cmd *exec.Cmd, hasher fs.Hasher, tapPath string, pidInfo func(int)) (*fs.TapCmdResult, error) {
	tapResult := &fs.TapCmdResult{}

	eventHandler := func(t *fs.TapFSEntry) {
		tapResult.TapEntries = append(tapResult.TapEntries, t)
	}

	info, err := RunTap(cmd, hasher, tapPath, pidInfo, eventHandler)
	if err != nil {
		return nil, err
	}
	tapResult.TapInfo = *info
	return tapResult, nil
}

func RunTapCmdResolved(cmd *exec.Cmd, casfs fs.FS, hasher fs.Hasher, tapPath string, pidInfo func(int)) (*fs.TapCmdResolvedResult, error) {
	out := fs.NewTapCmdResolvedResult()
	filterMap := make(map[string]struct{})
	tracker := fs.NewTapTrack()

	updateRead := func(t *fs.TapFSEntry) {
		// Check if path already exists as a write
		if _, filter := filterMap[t.Path]; filter {
			return
		}
		// Add to read path
		out.Reads[t.Path] = fs.BackendStat{
			IsARI: t.IsARI,
			ARI:   t.ARI,
			Hash:  t.Hash,
			Mode:  t.Mode,
			Size:  t.Size,
		}
	}
	updateWrite := func(t *fs.TapFSEntry) {
		filterMap[t.Path] = struct{}{}
		tracker.Add(t.Path, t.Mode)
	}
	removeWrite := func(path string) {
		tracker.Remove(path)
	}
	updateDelete := func(path string) {
		tracker.Remove(path)
		filterMap[path] = struct{}{}
		out.Deletes[path] = struct{}{}
	}

	eventHandler := func(t *fs.TapFSEntry) {
		switch t.Type {
		// Read events
		case fs.TapFSOpenRead, fs.TapFSStat, fs.TapFSSymlinkRead, fs.TapFSReadDir:
			updateRead(t)
		case fs.TapFSMkdir, fs.TapFSOpenWrite, fs.TapFSSymlink:
			// Add write data
			updateWrite(t)
		case fs.TapFSUnlink:
			// Remove any write record we have and add to delete
			removeWrite(t.Path)
			updateDelete(t.Path)
		case fs.TapFSRename:
			// Add delete and write record
			tracker.Rename(t.AuxPath, t.Path, t.Mode)
			filterMap[t.AuxPath] = struct{}{}
			filterMap[t.Path] = struct{}{}
			out.Deletes[t.AuxPath] = struct{}{}

		case fs.TapFSAttrChange:
			// Add a read record and a write record
			updateRead(t)
			updateWrite(t)
		}
	}

	info, err := RunTap(cmd, hasher, tapPath, pidInfo, eventHandler)
	if err != nil {
		return nil, err
	}
	hashStart := time.Now()

	// Hash write data by walking the tree
	walkFunc := func(path string, mode iofs.FileMode) error {
		if mode.IsDir() {
			// Don't hash directories, just add to write list
			out.Writes[path] = fs.BackendStat{
				Mode: mode,
				Hash: hasher.EmptyDirHash(),
			}
		} else {
		retry:
			bs, err := casfs.BackendStat(filepath.Join(casfs.MountPath(), path))
			if err != nil {
				if err == syscall.EINTR {
					goto retry
				}
				return err
			}
			out.Writes[path] = *bs
		}
		return nil
	}

	if err := tracker.Walk(walkFunc); err != nil {
		return nil, err
	}
	info.HashDuration = time.Since(hashStart).Seconds()
	out.TapInfo = *info
	return out, nil
}
