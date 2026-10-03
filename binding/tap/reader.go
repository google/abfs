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

package tap

import (
	"fmt"
	"io"
	"syscall"

	"golang.org/x/sys/unix"
)

type tapReader struct {
	fd            int
	abortChan     chan struct{}
	kernelScratch []byte
	bufStart      int // Start of unread data in kernelScratch
	bufEnd        int // End of valid data in kernelScratch
}

func tapReaderCreate(fd int) *tapReader {
	return &tapReader{
		fd:            fd,
		abortChan:     make(chan struct{}),
		kernelScratch: make([]byte, MaxChunkSize),
	}
}

func (r *tapReader) CmdDone() error {
	const IOCTL_TAP_CMD_FINISHED = 0x5401
	err := unix.IoctlSetInt(r.fd, IOCTL_TAP_CMD_FINISHED, 0)
	if err != nil {
		/* Safely signal stream abort exactly once across concurrent threads */
		close(r.abortChan)
		return fmt.Errorf("failed to send tap kernel finish signal due to ioctl handshake failure: %w", err)
	}
	return nil
}

func (r *tapReader) Read(b []byte) (int, error) {
	if r.bufStart < r.bufEnd {
		n := copy(b, r.kernelScratch[r.bufStart:r.bufEnd])
		r.bufStart += n
		return n, nil
	}

	select {
	case <-r.abortChan:
		return 0, fmt.Errorf("kernel tracking stream aborted")
	default:
	}

	/*
	 * Direct blocking read path for maximum throughput and minimum latency.
	 *
	 * Note: To abort a blocking read during shutdown, the caller should close
	 * the file descriptor, which will immediately unblock this thread.
	 */
	if len(b) >= len(r.kernelScratch) {
		for {
			n, err := unix.Read(r.fd, b)
			if err == syscall.EINTR {
				continue
			}
			if err != nil {
				return 0, err
			}
			if n == 0 {
				return 0, io.EOF
			}
			return n, nil
		}
	}

	r.bufStart = 0
	r.bufEnd = 0

	for {
		n, err := unix.Read(r.fd, r.kernelScratch)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, io.EOF
		}

		r.bufEnd = n
		break
	}

	n := copy(b, r.kernelScratch[r.bufStart:r.bufEnd])
	r.bufStart += n
	return n, nil
}
