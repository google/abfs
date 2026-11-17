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

package filebased

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/abfs/shared/entities"
)

// healthProbeFile is written and removed in the store directory by every
// health probe. Nothing in the server lists the store directory itself.
const healthProbeFile = ".abfs-health-probe"

// HealthProbes returns the backend health checks of the server by backend
// name.
func (s *fileBasedServer) HealthProbes() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{"store": s.checkStore}
}

// checkStore checks that the store directory is the one the server started
// with, that a small file can be written to it, and that a stored object can
// be read from disk. Disk I/O does not honour ctx.
func (s *fileBasedServer) checkStore(ctx context.Context) error {
	info, err := os.Stat(s.localDir)
	if err != nil {
		return fmt.Errorf("failed to stat the store directory: %w", err)
	}
	if !os.SameFile(info, s.localDirInfo) {
		return fmt.Errorf("store directory %s was replaced after the server started", s.localDir)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkWrite(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.checkRead()
}

// checkWrite creates, syncs and removes a small file in the store directory.
// It never creates directories, so it fails on a store directory that was
// moved away instead of making a new one.
func (s *fileBasedServer) checkWrite() (retErr error) {
	p := filepath.Join(s.localDir, healthProbeFile)
	// A crash between create and remove leaves the file behind.
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to remove the old probe file: %w", err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create the probe file: %w", err)
	}
	defer func() {
		if err := os.Remove(p); err != nil && retErr == nil {
			retErr = fmt.Errorf("failed to remove the probe file: %w", err)
		}
	}()
	if _, err := f.WriteString("abfs health probe\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to write the probe file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to sync the probe file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close the probe file: %w", err)
	}
	return nil
}

// checkRead reads the empty file object from disk, bypassing the header
// cache that answers most requests for it.
func (s *fileBasedServer) checkRead() (retErr error) {
	p := s.objectStore.Path(entities.EmptyFileHash)
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("failed to open the empty file object: %w", err)
	}
	defer entities.CloseErrCheck(f, &retErr)
	if _, err := io.Copy(io.Discard, f); err != nil {
		return fmt.Errorf("failed to read the empty file object: %w", err)
	}
	return nil
}
