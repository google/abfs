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
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/abfs/cas/hash/hashers"
)

func TestCloseDeadlock(t *testing.T) {
	// Create a temporary directory for the socket.
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Create a listener.
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer l.Close()

	// Server goroutine: accept connection but do NOT read from it.
	// This will eventually cause the client's write buffer to fill up.
	go func() {
		_, err := l.Accept()
		if err != nil {
			return
		}
		// Keep it open but do nothing.
		select {}
	}()

	// Create client.
	client := NewClient(socketPath, 1*time.Second)

	// Start serving.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- client.Serve(ctx)
	}()

	// We need to wait until the client is connected.
	for {
		client.writeLock.Lock()
		conn := client.conn
		client.writeLock.Unlock()
		if conn != nil {
			break
		}
		// Check if serve failed early
		select {
		case err := <-serveErr:
			t.Fatalf("Serve failed: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}

	hasher := hashers.CreateHasher(hashers.HasherTypeSHA1)
	h, err := hasher.NewHash("a9993e364706816aba3e25717850c26c9cd0d89d") // random sha1
	if err != nil {
		t.Fatalf("failed to create hash: %v", err)
	}

	// Now try to fill the write buffer.
	// We call SendNewObject repeatedly in a loop.
	// If it blocks, it will block holding the writeLock.
	sendError := make(chan error, 1)
	go func() {
		for {
			err := client.SendNewObject(h, TypeBlob)
			if err != nil {
				sendError <- err
				return
			}
		}
	}()

	// Give it some time to fill the buffer and block.
	time.Sleep(500 * time.Millisecond)

	// Check if sender already failed
	select {
	case err := <-sendError:
		t.Fatalf("Sender failed early: %v", err)
	default:
	}

	// Now try to Close the client.
	closeDone := make(chan struct{})
	go func() {
		t.Log("Calling Close()...")
		client.Close()
		t.Log("Close() returned")
		close(closeDone)
	}()

	select {
	case <-closeDone:
		t.Log("Close completed successfully (no deadlock)")
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK DETECTED: Close() did not return within 2 seconds")
	}
}
