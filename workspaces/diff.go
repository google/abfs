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

package workspaces

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

type DiffActionErr = struct {
	DiffAction
	Err error
}

func DirDiff(ctx context.Context, store entities.CASStore, a, b entities.Hash) <-chan DiffActionErr {
	ch := make(chan DiffActionErr)
	if a == b {
		close(ch)
		return ch
	}

	grp, ctx := errgroup.WithContext(ctx)
	grp.SetLimit(runtime.NumCPU())
	grp.Go(func() error {
		return diff(ctx, store, "", a, b, grp, ch)
	})

	go func() {
		defer close(ch)
		if err := grp.Wait(); err != nil {
			ch <- DiffActionErr{Err: err}
		}
	}()

	return ch
}

func diff(ctx context.Context, store entities.CASStore, p string, a, b entities.Hash, grp *errgroup.Group, ch chan<- DiffActionErr) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	aEnts, err := fetchTree(ctx, store, a)
	if err != nil {
		return err
	}
	bEnts, err := fetchTree(ctx, store, b)
	if err != nil {
		return err
	}

	for _, aEnt := range aEnts {
		bEnt, exists := bEnts[aEnt.Name]

		switch {
		case !exists:
			ch <- DiffActionErr{
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
			grp.Go(func() error {
				return diff(ctx, store, path.Join(p, aEnt.Name), aEnt.Hash, bEnt.Hash, grp, ch)
			})

		default:
			ch <- DiffActionErr{
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
			ch <- DiffActionErr{
				DiffAction: DiffAction{
					Path:  path.Join(p, bEnt.Name),
					HashB: bEnt.Hash,
					ModeB: bEnt.Mode,
					SizeB: bEnt.Size,
				},
			}
		}
	}

	return nil
}
