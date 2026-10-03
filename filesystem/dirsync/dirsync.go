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

package dirsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/store"
)

func DirSync(ctx context.Context, store entities.CASStore, dir string, curHash, targetHash entities.Hash) error {
	if curHash == targetHash {
		return nil
	}

	diffCh := DirDiff(ctx, store, curHash, targetHash, false)

	grp, ctx := errgroup.WithContext(ctx)
	grp.SetLimit(runtime.NumCPU())
	for item := range diffCh {
		grp.Go(func() error {
			if item.Err != nil {
				return item.Err
			}

			return syncItem(ctx, store, dir, item.DiffAction)
		})
	}

	return grp.Wait()
}

func DirSyncDiff(ctx context.Context, store entities.CASStore, dir string, diff []DiffAction) error {
	var deletedDirs []DiffAction
	grp, ctx := errgroup.WithContext(ctx)
	grp.SetLimit(runtime.NumCPU())
	for _, item := range diff {
		if item.ModeA.IsDir() && item.HashB.IsZero() {
			deletedDirs = append(deletedDirs, item)
			continue
		}
		grp.Go(func() error {
			return syncItem(ctx, store, dir, item)
		})
	}

	if err := grp.Wait(); err != nil {
		return err
	}

	sort.Slice(deletedDirs, func(i, j int) bool {
		return len(deletedDirs[i].Path) > len(deletedDirs[j].Path)
	})
	for _, item := range deletedDirs {
		_ = os.Remove(filepath.Join(dir, item.Path))
	}
	return nil
}

func syncItem(ctx context.Context, store entities.CASStore, dir string, action DiffAction) error {
	p := filepath.Join(dir, action.Path)

	if action.ModeA.IsFile() && action.ModeB.IsFile() {
		if action.HashA != action.HashB {
			if err := bindcas.SetHashSize(p, action.HashB, action.SizeB); err != nil {
				return err
			}
		}
		if action.ModeA != action.ModeB {
			if err := os.Chmod(p, os.FileMode(action.ModeB)&0777); err != nil {
				return err
			}
		}
		return nil
	}

	if !action.HashA.IsZero() {
		if err := removeLocal(p, action.ModeA); err != nil {
			return err
		}
	}
	if !action.HashB.IsZero() {
		if err := addEntry(ctx, store, p, action.HashB, action.ModeB, action.SizeB); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if err := os.MkdirAll(path.Dir(p), 0755); err != nil {
					log.Printf("failed to create parent directories for %s: %v", p, err)
				}
				err = addEntry(ctx, store, p, action.HashB, action.ModeB, action.SizeB)
			}
			return err
		}
	}
	return nil
}

func removeLocal(path string, mode entities.FileMode) error {
	if mode.IsDir() {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	} else {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func addEntry(ctx context.Context, casStore entities.CASStore, path string, hash entities.Hash, mode entities.FileMode, size int64) error {
	switch mode {
	case entities.FileModeDir:
		if err := os.Mkdir(path, 0755); err != nil {
			return err
		}
		return bindcas.SetHash(path, hash)

	case entities.FileModeSymlink:
		content, err := store.ReadAllBlob(ctx, casStore, hash)
		if err != nil {
			return err
		}
		return os.Symlink(string(content), path)

	case entities.FileModeSubmodule:
		if err := os.Mkdir(path, 0755); err != nil {
			return err
		}
		return nil

	case entities.FileModeRegular, entities.FileModeExecutable:
		if err := bindcas.Mknod(path, mode, 0); err != nil {
			return err
		}
		return bindcas.SetHashSize(path, hash, size)

	default:
		return fmt.Errorf("unknown file type %s", mode)
	}
}
