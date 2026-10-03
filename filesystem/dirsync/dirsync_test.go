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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/abfs/shared/entities"
)

type fakeCASStore struct {
	entities.CASStore
	trees map[entities.Hash]struct {
		content []byte
		meta    entities.TreeMeta
	}
}

func (f *fakeCASStore) GetTree(ctx context.Context, key entities.Hash) ([]byte, entities.TreeMeta, error) {
	if key == entities.EmptyDirHash {
		content, meta := encodeTestTree(nil)
		return content, meta, nil
	}
	t, ok := f.trees[key]
	if !ok {
		return nil, entities.TreeMeta{}, os.ErrNotExist
	}
	return t.content, t.meta, nil
}

func encodeTestTree(entries []entities.FileEntry) ([]byte, entities.TreeMeta) {
	var body bytes.Buffer
	var sizes []int64
	for _, e := range entries {
		fmt.Fprintf(&body, "%s %s\x00", e.Mode.String(), e.Name)
		body.Write(e.Hash[:])
		sizes = append(sizes, e.Size)
	}
	var full bytes.Buffer
	fmt.Fprintf(&full, "tree %d\x00", body.Len())
	full.Write(body.Bytes())
	return full.Bytes(), entities.TreeMeta{Sizes: sizes}
}

func TestDirSyncDiff_DeletedDirectoryLeavesEmptyDirBehind(t *testing.T) {
	ctx := context.Background()

	fileHash := entities.NewHash("1111111111111111111111111111111111111111")
	subDirHash := entities.NewHash("2222222222222222222222222222222222222222")
	treeAHash := entities.NewHash("3333333333333333333333333333333333333333")
	treeBHash := entities.NewHash("4444444444444444444444444444444444444444")

	store := &fakeCASStore{
		trees: make(map[entities.Hash]struct {
			content []byte
			meta    entities.TreeMeta
		}),
	}

	// Subdirectory "1" contains "IFoo.aidl"
	subContent, subMeta := encodeTestTree([]entities.FileEntry{
		{
			Name: "IFoo.aidl",
			Mode: entities.FileModeRegular,
			Hash: fileHash,
			Size: 10,
		},
	})
	store.trees[subDirHash] = struct {
		content []byte
		meta    entities.TreeMeta
	}{subContent, subMeta}

	// Tree A contains directory "1"
	treeAContent, treeAMeta := encodeTestTree([]entities.FileEntry{
		{
			Name: "1",
			Mode: entities.FileModeDir,
			Hash: subDirHash,
		},
	})
	store.trees[treeAHash] = struct {
		content []byte
		meta    entities.TreeMeta
	}{treeAContent, treeAMeta}

	// Tree B has deleted directory "1" (empty tree)
	treeBContent, treeBMeta := encodeTestTree(nil)
	store.trees[treeBHash] = struct {
		content []byte
		meta    entities.TreeMeta
	}{treeBContent, treeBMeta}

	// Create local filesystem state matching Tree A
	workDir := t.TempDir()
	dir1 := filepath.Join(workDir, "1")
	if err := os.MkdirAll(dir1, 0755); err != nil {
		t.Fatal(err)
	}
	file1 := filepath.Join(dir1, "IFoo.aidl")
	if err := os.WriteFile(file1, []byte("interface IFoo {}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate DiffHeadTree (which passes followDirs = true) + DirSyncDiff
	diff, err := DirDiffList(ctx, store, treeAHash, treeBHash, true /* followDirs */)
	if err != nil {
		t.Fatalf("DirDiffList failed: %v", err)
	}

	if err := DirSyncDiff(ctx, store, workDir, diff); err != nil {
		t.Fatalf("DirSyncDiff failed: %v", err)
	}

	// Verify file was removed
	if _, err := os.Stat(file1); !os.IsNotExist(err) {
		t.Fatalf("expected file %s to be deleted, got err=%v", file1, err)
	}

	// Verify directory "1" was also removed (and not left behind empty)
	if _, err := os.Stat(dir1); !os.IsNotExist(err) {
		t.Fatalf("BUG REPRODUCED: directory %s was left behind as an empty directory after sync!", dir1)
	}
}

func TestDirSyncDiff_NestedDeletedDirsAndPreservedEmptyDirs(t *testing.T) {
	ctx := context.Background()

	fileHash := entities.NewHash("1111111111111111111111111111111111111111")
	dirCHash := entities.NewHash("2222222222222222222222222222222222222222")
	dirBHash := entities.NewHash("3333333333333333333333333333333333333333")
	dirAHashOld := entities.NewHash("4444444444444444444444444444444444444444")
	dirAHashNew := entities.NewHash("5555555555555555555555555555555555555555")
	treeAHash := entities.NewHash("6666666666666666666666666666666666666666")
	treeBHash := entities.NewHash("7777777777777777777777777777777777777777")

	store := &fakeCASStore{
		trees: make(map[entities.Hash]struct {
			content []byte
			meta    entities.TreeMeta
		}),
	}

	// a/b/c contains file.txt
	cContent, cMeta := encodeTestTree([]entities.FileEntry{
		{Name: "file.txt", Mode: entities.FileModeRegular, Hash: fileHash, Size: 5},
	})
	store.trees[dirCHash] = struct {
		content []byte
		meta    entities.TreeMeta
	}{cContent, cMeta}

	// a/b contains directory c
	bContent, bMeta := encodeTestTree([]entities.FileEntry{
		{Name: "c", Mode: entities.FileModeDir, Hash: dirCHash},
	})
	store.trees[dirBHash] = struct {
		content []byte
		meta    entities.TreeMeta
	}{bContent, bMeta}

	// Old a contains b and keep.txt and empty_dir (with file)
	aOldContent, aOldMeta := encodeTestTree([]entities.FileEntry{
		{Name: "b", Mode: entities.FileModeDir, Hash: dirBHash},
		{Name: "empty_dir", Mode: entities.FileModeDir, Hash: dirCHash},
		{Name: "keep.txt", Mode: entities.FileModeRegular, Hash: fileHash, Size: 5},
	})
	store.trees[dirAHashOld] = struct {
		content []byte
		meta    entities.TreeMeta
	}{aOldContent, aOldMeta}

	// New a deletes b, keeps keep.txt, and makes empty_dir an intentionally empty directory
	aNewContent, aNewMeta := encodeTestTree([]entities.FileEntry{
		{Name: "empty_dir", Mode: entities.FileModeDir, Hash: entities.EmptyDirHash},
		{Name: "keep.txt", Mode: entities.FileModeRegular, Hash: fileHash, Size: 5},
	})
	store.trees[dirAHashNew] = struct {
		content []byte
		meta    entities.TreeMeta
	}{aNewContent, aNewMeta}

	treeAContent, treeAMeta := encodeTestTree([]entities.FileEntry{
		{Name: "a", Mode: entities.FileModeDir, Hash: dirAHashOld},
	})
	store.trees[treeAHash] = struct {
		content []byte
		meta    entities.TreeMeta
	}{treeAContent, treeAMeta}

	treeBContent, treeBMeta := encodeTestTree([]entities.FileEntry{
		{Name: "a", Mode: entities.FileModeDir, Hash: dirAHashNew},
	})
	store.trees[treeBHash] = struct {
		content []byte
		meta    entities.TreeMeta
	}{treeBContent, treeBMeta}

	workDir := t.TempDir()
	for _, f := range []string{
		filepath.Join(workDir, "a", "b", "c", "file.txt"),
		filepath.Join(workDir, "a", "empty_dir", "file.txt"),
		filepath.Join(workDir, "a", "keep.txt"),
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("hello"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	diff, err := DirDiffList(ctx, store, treeAHash, treeBHash, true)
	if err != nil {
		t.Fatalf("DirDiffList failed: %v", err)
	}

	if err := DirSyncDiff(ctx, store, workDir, diff); err != nil {
		t.Fatalf("DirSyncDiff failed: %v", err)
	}

	// Verify nested deleted directories a/b/c and a/b were removed
	for _, deletedDir := range []string{
		filepath.Join(workDir, "a", "b", "c"),
		filepath.Join(workDir, "a", "b"),
	} {
		if _, err := os.Stat(deletedDir); !os.IsNotExist(err) {
			t.Fatalf("expected deleted directory %s to be removed, got err=%v", deletedDir, err)
		}
	}

	// Verify directory a and keep.txt still exist
	if _, err := os.Stat(filepath.Join(workDir, "a", "keep.txt")); err != nil {
		t.Fatalf("expected a/keep.txt to exist, got err=%v", err)
	}

	// Verify intentionally empty directory a/empty_dir still exists on disk
	if info, err := os.Stat(filepath.Join(workDir, "a", "empty_dir")); err != nil || !info.IsDir() {
		t.Fatalf("expected intentionally empty directory a/empty_dir to be preserved, got err=%v", err)
	}
}
