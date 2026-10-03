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
	"path"
	"runtime"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
)

type DiffAction struct {
	Path  string
	HashA entities.Hash
	ModeA entities.FileMode
	SizeA int64
	HashB entities.Hash
	ModeB entities.FileMode
	SizeB int64
}

type DiffActionErr struct {
	DiffAction
	Err error
}

func DirDiffList(ctx context.Context, store entities.CASStore, a, b entities.Hash, followDirs bool) ([]DiffAction, error) {
	var actions []DiffAction
	for actionErr := range DirDiff(ctx, store, a, b, followDirs) {
		if actionErr.Err != nil {
			return nil, actionErr.Err
		}
		actions = append(actions, actionErr.DiffAction)
	}

	return actions, nil
}

type diffContext struct {
	store      entities.CASStore
	ch         chan DiffActionErr
	grp        *errgroup.Group
	followDirs bool
}

func DirDiff(ctx context.Context, store entities.CASStore, a, b entities.Hash, followDirs bool) <-chan DiffActionErr {
	ch := make(chan DiffActionErr)
	if a == b {
		close(ch)
		return ch
	}

	grp, ctx := errgroup.WithContext(ctx)
	diffContext := &diffContext{
		store:      store,
		ch:         ch,
		grp:        grp,
		followDirs: followDirs,
	}
	grp.SetLimit(runtime.NumCPU())
	grp.Go(func() error {
		return diffContext.diff(ctx, "", a, b)
	})

	go func() {
		defer close(ch)
		if err := grp.Wait(); err != nil {
			ch <- DiffActionErr{Err: err}
		}
	}()

	return ch
}

func (d *diffContext) Go(f func() error) error {
	if d.grp.TryGo(f) {
		return nil
	}
	return f()
}

func (d *diffContext) diff(ctx context.Context, p string, a, b entities.Hash) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	//TODO: Maybe fetch the two trees in parallel?
	aEnts, err := d.fetchTree(ctx, a)
	if err != nil {
		return err
	}
	bEnts, err := d.fetchTree(ctx, b)
	if err != nil {
		return err
	}

	for _, aEnt := range aEnts {
		bEnt, exists := bEnts[aEnt.Name]

		switch {
		case !exists:
			if d.followDirs && aEnt.Mode == entities.FileModeDir && aEnt.Hash != entities.EmptyDirHash {
				if err := d.Go(func() error {
					return d.pumpDirEntries(ctx, path.Join(p, aEnt.Name), aEnt.Hash, true)
				}); err != nil {
					return err
				}
			}
			// Always emit the DiffAction for the deleted entry itself even when followDirs is
			// true. Previously, recursing into pumpDirEntries skipped emitting the deleted
			// directory's own DiffAction, so DirSyncDiff unlinked all child files inside the
			// directory but never removed the directory itself.
			d.ch <- DiffActionErr{
				DiffAction: DiffAction{
					Path:  path.Join(p, aEnt.Name),
					HashA: aEnt.Hash,
					ModeA: aEnt.Mode,
					SizeA: aEnt.Size,
				},
			}

		case aEnt.Hash == bEnt.Hash && aEnt.Mode == bEnt.Mode:
			// do nothing

		case aEnt.Mode.IsDir() && bEnt.Mode.IsDir():
			if err := d.Go(func() error {
				return d.diff(ctx, path.Join(p, aEnt.Name), aEnt.Hash, bEnt.Hash)
			}); err != nil {
				return err
			}

		default:
			d.ch <- DiffActionErr{
				DiffAction: DiffAction{
					Path:  path.Join(p, aEnt.Name),
					HashA: aEnt.Hash,
					ModeA: aEnt.Mode,
					SizeA: aEnt.Size,
					HashB: bEnt.Hash,
					ModeB: bEnt.Mode,
					SizeB: bEnt.Size,
				},
			}
		}
	}
	for _, bEnt := range bEnts {
		if _, exists := aEnts[bEnt.Name]; !exists {
			if d.followDirs && bEnt.Mode == entities.FileModeDir && bEnt.Hash != entities.EmptyDirHash {
				if err := d.Go(func() error {
					return d.pumpDirEntries(ctx, path.Join(p, bEnt.Name), bEnt.Hash, false)
				}); err != nil {
					return err
				}
			} else {
				d.ch <- DiffActionErr{
					DiffAction: DiffAction{
						Path:  path.Join(p, bEnt.Name),
						HashB: bEnt.Hash,
						ModeB: bEnt.Mode,
						SizeB: bEnt.Size,
					},
				}
			}
		}
	}

	return nil
}

func (d *diffContext) fetchTree(ctx context.Context, targetHash entities.Hash) (map[string]entities.FileEntry, error) {
	content, meta, err := d.store.GetTree(ctx, targetHash)
	if err != nil {
		return nil, err
	}
	entries, err := entities.DecodeDir(content, meta)
	if err != nil {
		return nil, err
	}
	fileEntries := make(map[string]entities.FileEntry, len(entries))
	for _, e := range entries {
		fileEntries[e.Name] = e
	}
	return fileEntries, nil
}

func (d *diffContext) pumpDirEntries(ctx context.Context, p string, dirHash entities.Hash, isA bool) error {
	ents, err := d.fetchTree(ctx, dirHash)
	if err != nil {
		return err
	}
	for _, ent := range ents {
		if ent.Mode.IsDir() && ent.Hash != entities.EmptyDirHash {
			if err := d.Go(func() error {
				return d.pumpDirEntries(ctx, path.Join(p, ent.Name), ent.Hash, isA)
			}); err != nil {
				return err
			}
			// When adding a new directory tree (!isA), skip emitting DiffActions for non-empty
			// intermediate directories because syncItem creates missing parent directories via
			// os.MkdirAll when creating child files (and emitting directory creation actions
			// concurrently would race with os.MkdirAll).
			// When deleting a directory tree (isA), do NOT continue so that every nested
			// subdirectory has its deletion DiffAction emitted and can be removed deepest-first.
			if !isA {
				continue
			}
		}
		if isA {
			d.ch <- DiffActionErr{
				DiffAction: DiffAction{
					Path:  path.Join(p, ent.Name),
					HashA: ent.Hash,
					ModeA: ent.Mode,
					SizeA: ent.Size,
				},
			}
		} else {
			d.ch <- DiffActionErr{
				DiffAction: DiffAction{
					Path:  path.Join(p, ent.Name),
					HashB: ent.Hash,
					ModeB: ent.Mode,
					SizeB: ent.Size,
				},
			}
		}
	}
	return nil
}
