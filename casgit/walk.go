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

package casgit

import (
	"context"
	"errors"
	"path"

	"github.com/google/abfs/shared/entities"
)

var (
	ErrWalkStop     = errors.New("stop the walk")
	ErrWalkNoStepIn = errors.New("do not step into dir")
)

type WalkFunc func(path string, mode entities.FileMode, hash entities.Hash) error

func Walk(ctx context.Context, dirHash entities.Hash, store entities.CASStore, visit WalkFunc) error {
	err := walk(ctx, dirHash, ".", store, visit)
	if err == ErrWalkStop || err == ErrWalkNoStepIn {
		return nil
	}
	return err
}

func walk(ctx context.Context, dirHash entities.Hash, p string, store entities.CASStore, visit WalkFunc) error {
	dirBytes, sizes, err := store.GetTree(ctx, dirHash)
	if err != nil {
		return err
	}

	entries, err := entities.DecodeDir(dirBytes, sizes)
	if err != nil {
		return err
	}

	for _, ent := range entries {
		entPath := path.Join(p, ent.Name)
		err := visit(entPath, ent.Mode, ent.Hash)
		if err != nil && err != ErrWalkNoStepIn {
			return err
		}
		if ent.Mode == entities.FileModeDir && err != ErrWalkNoStepIn {
			if err := walk(ctx, ent.Hash, entPath, store, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
