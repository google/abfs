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

package importer

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
)

type NodeType int

const (
	FileNode NodeType = iota
	DirNode
	SymlinkNode
)

type FinalizeResult struct {
	Path    string
	Type    NodeType
	Skipped bool
	Err     error
}

type CopyOptions struct {
	Src        string
	Dst        string
	MaxWorkers int
	Force      bool
	Preserve   bool // maintain timestamps
	Finalize   func(FinalizeResult) error
}

type Stats struct {
	ActiveWorkers int32
	TotalBytes    int64 // Total bytes discovered
	CopiedBytes   int64 // Total bytes actually read/written
	TotalFiles    int64
	Completed     int64 // Successfully processed files/links
	SkippedCount  int64 // Explicitly tracked skips
}

// Just do a top down copy and hashing of the files found.
// We will leave out hashing the directories as this requires going from the bottom up
// And instead will rely on the casfs to do this when we ask for the root hash.
func ParallelCopyTopDown(opt CopyOptions) (*Stats, error) {
	stats := &Stats{}
	startTime := time.Now()

	g, ctx := errgroup.WithContext(context.Background())
	paths := make(chan string, opt.MaxWorkers*2)

	stopMonitor := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				active := atomic.LoadInt32(&stats.ActiveWorkers)
				done := atomic.LoadInt64(&stats.Completed)
				skip := atomic.LoadInt64(&stats.SkippedCount)
				total := atomic.LoadInt64(&stats.TotalFiles)
				copied := atomic.LoadInt64(&stats.CopiedBytes)

				rate := (float64(copied) / 1024 / 1024) / time.Since(startTime).Seconds()
				fmt.Printf("\r\033[K Active: %d | Rate: %.2f MB/s | Processed: %d (Total Discovered: %d)",
					active, rate, done+skip, total)
			case <-stopMonitor:
				return
			}
		}
	}()

	for i := 0; i < opt.MaxWorkers; i++ {
		g.Go(func() error {
			for path := range paths {
				atomic.AddInt32(&stats.ActiveWorkers, 1)
				err := doCopy(ctx, path, opt, stats)
				atomic.AddInt32(&stats.ActiveWorkers, -1)
				if err != nil {
					return err
				}
			}
			return nil
		})
	}

	// We run the walker in a separate goroutine so workers can start immediately
	g.Go(func() error {
		defer close(paths)
		return filepath.WalkDir(opt.Src, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if d.IsDir() {
				rel, _ := filepath.Rel(opt.Src, path)
				target := filepath.Join(opt.Dst, rel)
				info, _ := d.Info()
				if err := os.MkdirAll(target, info.Mode()); err != nil {
					return err
				}
				return nil
			}

			atomic.AddInt64(&stats.TotalFiles, 1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case paths <- path:
			}
			return nil
		})
	})

	err := g.Wait()
	close(stopMonitor)
	fmt.Print("\n")
	return stats, err
}

func doCopy(ctx context.Context, path string, opt CopyOptions, stats *Stats) error {
	rel, _ := filepath.Rel(opt.Src, path)
	target := filepath.Join(opt.Dst, rel)

	info, err := os.Lstat(path)
	if err != nil {
		return err
	}

	var res FinalizeResult
	res.Path = target

	if info.Mode()&os.ModeSymlink != 0 {
		res.Type = SymlinkNode
		err = copySymlink(path, target)
		atomic.AddInt64(&stats.Completed, 1)
	} else {
		res.Type = FileNode
		skipped, b, cErr := copyFile(path, target, info.Mode(), opt)
		err = cErr
		if err == nil {
			if skipped {
				atomic.AddInt64(&stats.SkippedCount, 1)
			} else {
				atomic.AddInt64(&stats.CopiedBytes, b)
				atomic.AddInt64(&stats.Completed, 1)
			}
		}
	}

	if opt.Finalize != nil {
		fErr := opt.Finalize(res)
		if fErr != nil {
			return fErr
		}
	}
	return err
}

func copyFile(src, dst string, mode os.FileMode, opt CopyOptions) (skipped bool, size int64, retErr error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false, 0, err
	}
	srcSize := srcInfo.Size()

	dstInfo, err := os.Stat(dst)
	if err == nil {
		if !opt.Force {
			if srcSize == dstInfo.Size() && srcInfo.ModTime().Equal(dstInfo.ModTime()) {
				return true, srcSize, nil
			}
		}
		// Handle Read-Only Destination
		if dstInfo.Mode().Perm()&0200 == 0 {
			if opt.Preserve {
				if err := os.Chmod(dst, 0666); err != nil {
					return false, 0, fmt.Errorf("failed to chmod read-only file: %w", err)
				}
			} else if opt.Force {
				if err := os.Remove(dst); err != nil {
					return false, 0, fmt.Errorf("failed to remove read only file: %w", err)
				}
			}

		}
	}

	sF, err := os.Open(src)
	if err != nil {
		return false, 0, err
	}
	defer func() { retErr = entities.FirstError(retErr, sF.Close()) }()

	dF, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return false, 0, err
	}
	// Failsafe to close file in early exit condition.  The file is explicitly closed below, this close
	// can safely ignore error code.
	defer func() { retErr = entities.FirstError(retErr, dF.Close()) }()

	n, err := io.Copy(dF, sF)
	if err != nil {
		return false, n, err
	}

	if err := dF.Sync(); err != nil {
		return false, 0, err
	}
	if err := dF.Close(); err != nil {
		return false, 0, err
	}

	if opt.Preserve {
		// Restore original source timestamps and permissions
		if err := os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime()); err != nil {
			return false, 0, err
		}
	}
	return false, n, nil
}

func copySymlink(src, dst string) error {
	link, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !entities.IsNotExist(err) {
		return err
	}
	return os.Symlink(link, dst)
}
