// Copyright 2025 Google LLC
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

package tests

import (
	"context"
	"fmt"
	iofs "io/fs"
	"path/filepath"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse"
)

func testDentry(fs casfuse.FS, name string) casfuse.Dentry {
	return fs.Dcache().Create(nil, name)

}
func testDentryWithParent(fs casfuse.FS, parent casfuse.Dentry, name string) casfuse.Dentry {
	return fs.Dcache().Create(parent, name)
}

// Add symlink to top level dir
func CASResolve(ctx context.Context, dir casfuse.Dir, casfsApi casfuse.API, store cas.Store) error {
	var mode iofs.FileMode

	target := testDentry(dir.FS(), "target")
	symlink := testDentry(dir.FS(), "symlink")

	inode, err := dir.CreateFile(ctx, target, mode)
	if err != nil {
		return err
	}
	target.Instantiate(inode)
	// Create link
	if inode, err := dir.Symlink(ctx, symlink, target.Name()); err != nil {
		return err
	} else {
		symlink.Instantiate(inode)
	}

	// Grab dir hash
	h, err := casfsApi.Hash(ctx, dir)
	if err != nil {
		return err
	}
	de, err := cas.Resolve(store, h, "symlink")
	if err != nil {
		return err
	}
	// Grab target hash
	tHash, err := casfsApi.Hash(ctx, target.Inode())
	if err != nil {
		return err
	}
	if !de.Hash.Equals(tHash) {
		return fmt.Errorf("expected hash %s got %s", tHash, de.Hash)
	}
	return nil

}

// Add symlink to top level dir
func CASResolveDeep(ctx context.Context, dir casfuse.Dir, casfsApi casfuse.API, store cas.Store) error {
	var tgtPath string
	topDir := dir
	var parent casfuse.Dentry
	for _ = range 100 {
		dentry := testDentryWithParent(dir.FS(), parent, "test-dir")
		if inode, err := dir.Mkdir(ctx, dentry, cas.FileModeDir()); err != nil {
			return fmt.Errorf("mkdir err: %s err", err)
		} else {
			dentry.Instantiate(inode)
		}
		dir = dentry.Inode().(casfuse.Dir)
		tgtPath = filepath.Join(tgtPath, "test-dir")
		parent = dentry
	}

	mode := cas.FileModeFile()
	target := testDentryWithParent(dir.FS(), parent, "target")
	if inode, err := dir.CreateFile(ctx, target, mode); err != nil {
		return err
	} else {
		target.Instantiate(inode)
	}
	// Grab target hash
	tHash, err := casfsApi.Hash(ctx, target.Inode())

	dentry := testDentryWithParent(dir.FS(), parent, "symlink")
	// Create link
	if inode, err := dir.Symlink(ctx, dentry, "target"); err != nil {
		return err
	} else {
		dentry.Instantiate(inode)
	}

	// Grab dir hash
	h, err := casfsApi.Hash(ctx, topDir)
	if err != nil {
		return err
	}
	tgtPath = filepath.Join(tgtPath, "symlink")
	de, err := cas.Resolve(store, h, tgtPath)
	if err != nil {
		return err
	}
	if !de.Hash.Equals(tHash) {
		return fmt.Errorf("expected hash %s got %s", tHash, de.Hash)
	}

	return nil

}
