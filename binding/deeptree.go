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
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/maps"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
)

const (
	CurrentDeepTreeRequestVersion                     = objects.CurrentDeepTreeRequestVersion
	ObjectTypeDeepTreeRequest     entities.ObjectType = objects.ObjectTypeDeepTreeRequest
	DeepTreeThreshold                                 = 1000
)

type FileEntry = entities.FileEntry

type DeepTreeRequest = objects.DeepTreeRequest

func PutDeepTree(ctx context.Context, entries []*entities.DeepEntry, store entities.CASStore) (entities.Hash, error) {
	deepTree := &gitobjects.DeepTreeObject{DeepTree: entities.DeepTree{Entries: entries}}
	if err := deepTree.Encode(); err != nil {
		return entities.Hash{}, err
	}
	if err := store.PutObject(ctx, deepTree.Hash, deepTree.Encoded); err != nil {
		return entities.Hash{}, err
	}
	//TODO add map
	return deepTree.Hash, nil
}

type treePromise struct {
	sync.Once
	entries []FileEntry
}

type treeFetcher struct {
	lock  sync.Mutex
	store entities.CASStore
	trees map[entities.Hash]*treePromise
	sem   chan struct{}
	err   error
}

func newTreeFetcher(store entities.CASStore) *treeFetcher {
	return &treeFetcher{
		store: store,
		sem:   make(chan struct{}, runtime.NumCPU()),
		trees: make(map[entities.Hash]*treePromise),
	}
}

func (f *treeFetcher) setError(err error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	if f.err != nil {
		return
	}

	f.err = err
}

func (f *treeFetcher) error() error {
	f.lock.Lock()
	defer f.lock.Unlock()

	return f.err
}

func (f *treeFetcher) treePromise(hash entities.Hash) *treePromise {
	f.lock.Lock()
	defer f.lock.Unlock()

	promise, exists := f.trees[hash]
	if exists {
		return promise
	}
	promise = new(treePromise)
	f.trees[hash] = promise
	return promise
}

func (f *treeFetcher) tree(hash entities.Hash) ([]FileEntry, error) {
	promise := f.treePromise(hash)
	promise.Do(func() {
		if f.error() != nil {
			return
		}
		entries, err := f.handle(hash)
		if err != nil {
			f.setError(fmt.Errorf("hash: %v err: %w", hash, err))
		}
		promise.entries = entries
	})
	return promise.entries, f.err
}

func (f *treeFetcher) fetchTree(hash entities.Hash) ([]byte, entities.TreeMeta, error) {
	f.sem <- struct{}{}
	defer func() { <-f.sem }()

	return f.store.GetTree(context.Background(), hash)
}

func (f *treeFetcher) handle(hash entities.Hash) ([]FileEntry, error) {
	treeBytes, sizes, err := f.fetchTree(hash)
	if err != nil {
		return nil, err
	}

	entries, err := entities.DecodeDir(treeBytes, sizes)
	if err != nil {
		return nil, err
	}
	var group errgroup.Group
	for _, ent := range entries {
		if ent.Mode == entities.FileModeDir {
			group.Go(func() error {
				_, err := f.tree(ent.Hash)
				return err
			})
		}
	}
	return entries, group.Wait()
}

func CreateDeepTree(tree entities.Hash, store entities.CASStore) (*gitobjects.DeepTreeObject, error) {
	fetcher := newTreeFetcher(store)

	entries := []*entities.DeepEntry{{
		Mode: entities.FileModeDir,
		Hash: tree,
	}}

	ctx := context.Background()
	var dfs func(treeEnt *entities.DeepEntry) error
	dfs = func(treeEnt *entities.DeepEntry) error {
		treeEntries, err := fetcher.tree(treeEnt.Hash)
		if err != nil {
			return err
		}
		treeEnt.Size++
		for _, ent := range treeEntries {
			start := len(entries)
			child := &entities.DeepEntry{
				Name: ent.Name,
				Mode: ent.Mode,
				Hash: ent.Hash,
			}
			entries = append(entries, child)
			if ent.Mode == entities.FileModeDir {
				if err := dfs(child); err != nil {
					return err
				}
				if child.Size > DeepTreeThreshold {
					child.Name = ""
					hash, err := PutDeepTree(ctx, entries[start:], store)
					if err != nil {
						return err
					}
					entries = append(entries[:start], &entities.DeepEntry{
						Name: ent.Name,
						Mode: entities.FileModeDeepTree,
						Hash: hash,
					})
					treeEnt.Size++
				} else {
					treeEnt.Size += child.Size
				}
			} else {
				child.Size = ent.Size
				treeEnt.Size++
			}
		}
		return nil
	}
	if err := dfs(entries[0]); err != nil {
		return nil, err
	}
	return &gitobjects.DeepTreeObject{
		DeepTree: entities.DeepTree{Entries: entries},
	}, nil
}

func NewDeepTreeRequest(tree entities.Hash, store entities.CASStore) *maps.MapRequest {
	input := &DeepTreeRequest{
		Version: CurrentDeepTreeRequestVersion,
		Tree:    tree,
	}

	return &maps.MapRequest{
		Input: input,
		MapFunc: func(mo maps.MapObject) (maps.MapObject, error) {
			return CreateDeepTree(tree, store)
		},
		OutputDecode: func(hash Hash, content []byte) (maps.MapObject, error) {
			deepTree := new(gitobjects.DeepTreeObject)
			if err := deepTree.Decode(hash, content); err != nil {
				return nil, err
			}
			return deepTree, nil
		},
		Store: store,
	}
}

func UnpackDeepTree(ctx context.Context, deeptree *gitobjects.DeepTreeObject, store entities.CASStore) (int64, int64, error) {
	pushersGrp, ctx := errgroup.WithContext(ctx)
	pushersGrp.SetLimit(8000)

	var deepTreeCount atomic.Int64
	var dirCount atomic.Int64
	deepTreeCount.Store(1)

	encodersGrp, ctx := errgroup.WithContext(ctx)
	encodersGrp.SetLimit(runtime.NumCPU())
	var dfs func(entries []*entities.DeepEntry) error
	tryDFS := func(entries []*entities.DeepEntry) error {
		if encodersGrp.TryGo(func() error {
			return dfs(entries)
		}) {
			return nil
		}
		return dfs(entries)
	}
	dfs = func(entries []*entities.DeepEntry) error {
		treeHash := entries[0].Hash
		treeEntries := make([]entities.FileEntry, 0)
		for i := 1; i < len(entries); i++ {
			ent := entries[i]
			treeEntries = append(treeEntries, entities.FileEntry{
				Name: ent.Name,
				Hash: ent.Hash,
				Mode: ent.Mode,
			})

			if ent.Mode == entities.FileModeDir {
				if err := tryDFS(entries[i : i+int(ent.Size)]); err != nil {
					return err
				}
				i += int(ent.Size) - 1
			}
			if ent.Mode == entities.FileModeDeepTree {
				deepTreeCount.Add(1)
				deeptree, err := FetchObject[gitobjects.DeepTreeObject](ctx, store, ent.Hash)
				if err != nil {
					return err
				}
				if err := tryDFS(deeptree.Entries); err != nil {
					return err
				}
				// The last treeEntry is a deep-tree, but we need the tree object associated with it.
				// The tree object of a deeptree is always the first entry of it and update the type
				// to a tree (instead of the deep-tree).
				lastEntry := &treeEntries[len(treeEntries)-1]
				lastEntry.Hash = deeptree.Entries[0].Hash
				lastEntry.Mode = entities.FileModeDir
			}
		}
		buf, err := gitobjects.EncodeSortedTree(treeEntries)
		if err != nil {
			return err
		}
		hash := objects.HashBytes(buf)
		if hash != treeHash {
			for _, ent := range treeEntries {
				fmt.Println(treeHash, "-->", ent.Mode, ent.Name, ent.Hash)
			}
			return fmt.Errorf("tree hash mismatch: %s != %s", hash, treeHash)
		}
		dirCount.Add(1)
		pushersGrp.Go(func() error {
			return store.PutObject(ctx, hash, buf)
		})
		return nil
	}
	if err := dfs(deeptree.Entries); err != nil {
		return 0, 0, err
	}
	if err := encodersGrp.Wait(); err != nil {
		return 0, 0, err
	}
	if err := pushersGrp.Wait(); err != nil {
		return 0, 0, err
	}
	return deepTreeCount.Load(), dirCount.Load(), nil
}

func DeepTreeForTree(ctx context.Context, tree entities.Hash, store entities.CASStore) (*gitobjects.DeepTreeObject, error) {
	mapObj, err := maps.Map(ctx, NewDeepTreeRequest(tree, store))
	if err != nil {
		return nil, err
	}
	return mapObj.(*gitobjects.DeepTreeObject), nil
}
