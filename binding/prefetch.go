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

package binding

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
)

const (
	ContentPrefetchPriority  = 0.3
	PrefetchSymlinksPriority = 0.3
)

func PrefetchDirs(dirPatterns []string, cacheman *client.Client, priority float64) error {
	blobs := make([]entities.Hash, 0)
	for _, pattern := range dirPatterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil {
				continue
			}
			if info.IsDir() {
				if err := prefetchDir(m, cacheman, priority); err != nil {
					return err
				}
			} else {
				dirty, _ := bindcas.Dirty(m) // ignore errors
				if dirty {
					return nil
				}
				hash, _ := bindcas.Hash(m) // ignore errors
				if !hash.IsZero() {
					blobs = append(blobs, hash)
				}
			}
		}
	}
	return cacheman.Prefetch(blobs, nil, ContentPrefetchPriority)
}

func prefetchDir(dir string, cacheman *client.Client, priority float64) error {
	blobs := make([]entities.Hash, 0)
	err := filepath.Walk(dir, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if p != dir {
				excluded, _ := bindcas.Excluded(p) // ignore errors
				if excluded {
					return filepath.SkipDir
				}
			}
			return nil
		}
		dirty, _ := bindcas.Dirty(p) // ignore errors
		if dirty {
			return nil
		}
		hash, _ := bindcas.Hash(p) // ignore errors
		if !hash.IsZero() {
			blobs = append(blobs, hash)
		}
		return nil
	})
	if err != nil {
		return err
	}

	return cacheman.Prefetch(blobs, nil, priority)
}

func PrefetchSymlinks(ctx context.Context, deepTree *gitobjects.DeepTreeObject, store entities.CASStore, cacheman *client.Client) error {
	var grp errgroup.Group
	grp.SetLimit(runtime.NumCPU())
	if err := prefetchSymlinks(ctx, deepTree, store, cacheman, &grp); err != nil {
		return err
	}
	return grp.Wait()
}

func prefetchSymlinks(ctx context.Context, deepTree *gitobjects.DeepTreeObject, store entities.CASStore, cacheman *client.Client, grp *errgroup.Group) error {
	blobs := make([]entities.Hash, 0)
	for _, ent := range deepTree.Entries {
		switch ent.Mode {
		case entities.FileModeSymlink:
			blobs = append(blobs, ent.Hash)
		case entities.FileModeDeepTree:
			fn := func() error {
				dt, err := FetchObject[gitobjects.DeepTreeObject](ctx, store, ent.Hash)
				if err != nil {
					return err
				}
				return prefetchSymlinks(ctx, dt, store, cacheman, grp)
			}
			if !grp.TryGo(fn) {
				if err := fn(); err != nil {
					return err
				}
			}
		}
	}
	return cacheman.Prefetch(blobs, nil, PrefetchSymlinksPriority)
}
