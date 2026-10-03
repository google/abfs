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
	"reflect"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse"
)

func testEntryAndLookup(ctx context.Context, dir casfuse.Dir, name string, expMode cas.FileMode) error {
	nodes, err := dir.Entries(ctx)
	if err != nil {
		return err
	}

	var inode casfuse.Inode
	if i, ok := nodes[name]; ok {
		if i.Mode() != expMode {
			return fmt.Errorf("expected %s got %s", expMode, i.Mode())
		}
		inode = i
	}
	if inode == nil {
		return fmt.Errorf("failed to find %s in entries", name)
	}
	lkupNode, err := dir.Lookup(ctx, testDentry(dir.FS(), name))
	if err != nil {
		return err
	}
	if lkupNode != inode {
		return fmt.Errorf("entry inode %t mismatch lookup inode %t", inode, lkupNode)
	}
	attr := lkupNode.Attr(ctx, nil)
	if attr.Ino != lkupNode.Ino() {
		return fmt.Errorf("inumber mismatch %d %d", attr.Ino, lkupNode.Ino())
	}
	if attr.Size != lkupNode.Size() {
		return fmt.Errorf("size mismatch %d %d", attr.Size, lkupNode.Size())
	}

	lkupNode.Ino()
	lkupNode.Size()
	lkupNode.CanCache()

	return nil
}

func testEntryAndLookupNonExisting(ctx context.Context, dir casfuse.Dir, name string) error {
	nodes, err := dir.Entries(ctx)
	if err != nil {
		return err
	}

	if _, ok := nodes[name]; ok {
		return fmt.Errorf("found %s", name)
	}
	_, err = dir.Lookup(ctx, testDentry(dir.FS(), name))
	if err == nil {
		return fmt.Errorf("lookup %s", name)
	}
	return nil
}

func FsMkdirTest(ctx context.Context, dir casfuse.Dir, name string) error {
	dentry := testDentry(dir.FS(), name)
	inode, err := dir.Mkdir(ctx, dentry, cas.FileModeDir())
	if err != nil {
		return err
	}
	if inode.Mode() != cas.FileModeDir() {
		return fmt.Errorf("got a file object for node")
	}
	// Make sure we get name via lookup and entries
	return testEntryAndLookup(ctx, dir, name, cas.FileModeDir())
}
func FsRmdirTest(ctx context.Context, dir casfuse.Dir, name string) error {
	err := FsMkdirTest(ctx, dir, name)
	if err != nil {
		return err
	}
	err = dir.Remove(ctx, testDentry(dir.FS(), name))
	if err != nil {
		return err
	}
	return testEntryAndLookupNonExisting(ctx, dir, name)
}
func FsRmdirNonExists(ctx context.Context, dir casfuse.Dir) error {
	err := dir.Remove(ctx, testDentry(dir.FS(), "doesnotexists"))
	if err == nil {
		return fmt.Errorf("expected failure didn't get it")
	}
	return nil

}
func FsCreateTmpFile(ctx context.Context, dir casfuse.Dir, mode cas.FileMode) error {
	entries, err := dir.Entries(ctx)
	if err != nil {
		return err
	}
	dentry := testDentry(dir.FS(), "")
	inode, err := dir.CreateTmpFile(ctx, dentry, mode)
	if err != nil {
		return err
	}
	if inode.Mode() != mode {
		return fmt.Errorf("expected object type %s got %s", mode, inode.Mode())
	}
	entries2, err := dir.Entries(ctx)
	if err != nil {
		return err
	}
	fmt.Println(entries)
	fmt.Println(entries2)
	if !reflect.DeepEqual(entries, entries2) {
		return fmt.Errorf("entries changed after creating tmp file")
	}
	return nil
}
func FsCreateFile(ctx context.Context, dir casfuse.Dir, name string, mode cas.FileMode) error {
	dentry := testDentry(dir.FS(), name)
	inode, err := dir.CreateFile(ctx, dentry, mode)
	if err != nil {
		return err
	}
	// Make sure we get name via lookup and entries
	if err := testEntryAndLookup(ctx, dir, name, mode); err != nil {
		return err
	}
	if _, ok := inode.(casfuse.File); !ok {
		return fmt.Errorf("expected file object didn't get one")
	}
	return nil

}
func FsCreateSymlink(ctx context.Context, dir casfuse.Dir, name, target string) error {
	dentry := testDentry(dir.FS(), name)
	inode, err := dir.Symlink(ctx, dentry, target)
	if err != nil {
		return err
	}
	// Make sure we get name via lookup and entries
	expMode := cas.FileModeSymLink()
	if err := testEntryAndLookup(ctx, dir, name, expMode); err != nil {
		return err
	}
	if sym, ok := inode.(casfuse.SymLink); !ok {
		fmt.Printf("expected symlink object didn't get one")
	} else {
		t, err := sym.Target(ctx, nil)
		if err != nil {
			return err
		}
		if t != target {
			return fmt.Errorf("expected target %s got %s", target, t)
		}
	}
	return nil
}

func FsSetSymlink(ctx context.Context, dir casfuse.Dir, name, target, target2 string) error {
	dentry := testDentry(dir.FS(), name)
	inode, err := dir.Symlink(ctx, dentry, target)
	if err != nil {
		return err
	}

	if sym, ok := inode.(casfuse.SymLink); !ok {
		fmt.Printf("expected symlink object didn't get one")
	} else {
		sym.SetTarget(ctx, target2)
		t, err := sym.Target(ctx, nil)
		if err != nil {
			return err
		}
		if t != target2 {
			return fmt.Errorf("expected target %s got %s", target2, t)
		}
	}
	return nil
}

func FsRename(ctx context.Context, oldDir casfuse.Dir, oldName string, newDir casfuse.Dir, newName string, expMode cas.FileMode) error {
	if err := oldDir.Rename(ctx, testDentry(oldDir.FS(), oldName), newDir, testDentry(oldDir.FS(), newName)); err != nil {
		return err
	}
	if err := testEntryAndLookupNonExisting(ctx, oldDir, oldName); err != nil {
		return err
	}

	return testEntryAndLookup(ctx, newDir, newName, expMode)
}

func FsRenameTest(ctx context.Context, dir casfuse.Dir) error {

	dir1 := testDentry(dir.FS(), "dir1")
	dir2 := testDentry(dir.FS(), "dir2")
	if inode, err := dir.Mkdir(ctx, dir1, cas.FileModeDir()); err != nil {
		return err
	} else {
		dir1.Instantiate(inode)
	}
	if inode, err := dir.Mkdir(ctx, dir2, cas.FileModeDir()); err != nil {
		return err
	} else {
		dir2.Instantiate(inode)
	}
	var d1, d2 casfuse.Dir
	var ok bool

	d1, ok = dir1.Inode().(casfuse.Dir)
	if !ok {
		return fmt.Errorf("failed to get dir object from inode1")
	}
	d2, ok = dir2.Inode().(casfuse.Dir)
	if !ok {
		return fmt.Errorf("failed to get dir object from inode2")
	}
	mode := cas.FileModeFile()
	file1 := testDentry(dir.FS(), "file")
	file2 := testDentry(dir.FS(), "f2")
	inode, err := d1.CreateFile(ctx, file1, mode)
	if err != nil {
		return err
	} else {
		file1.Instantiate(inode)
	}

	if err := d1.Rename(ctx, file1, d2, file2); err != nil {
		return err
	}

	if err := testEntryAndLookupNonExisting(ctx, d1, "file"); err != nil {
		return err
	}
	if err := testEntryAndLookup(ctx, d2, "f2", cas.FileModeFile()); err != nil {

		return err
	}
	// Rename back to original
	if err := d2.Rename(ctx, file2, d1, file1); err != nil {
		return err
	}

	if err := testEntryAndLookupNonExisting(ctx, d2, "f2"); err != nil {
		return err
	}
	if err := testEntryAndLookup(ctx, d1, "file", cas.FileModeFile()); err != nil {
		return err
	}

	return nil

}
func FsMknod(ctx context.Context, dir casfuse.Dir, name string, mode cas.FileMode, rDev int) error {
	dentry := testDentry(dir.FS(), name)
	inode, err := dir.Mknod(ctx, dentry, mode, rDev)
	if err != nil {
		return err
	}
	if mode != inode.Mode() {
		return fmt.Errorf("expected %s got %s", mode, dentry.Inode().Mode())
	}
	// Make sure we get name via lookup and entries
	return testEntryAndLookup(ctx, dir, name, mode)
}

func FsSimpleLink(ctx context.Context, dir casfuse.Dir, name1, name2 string) error {

	var mode iofs.FileMode
	dentry1 := testDentry(dir.FS(), name1)
	dentry2 := testDentry(dir.FS(), name2)
	if inode, err := dir.CreateFile(ctx, dentry1, mode); err != nil {
		return err
	} else {
		dentry1.Instantiate(inode)
	}
	inode, err := dir.Link(ctx, dentry1, dentry2)
	if err != nil {
		return err
	}
	// Make sure we get name via lookup and entries
	return testEntryAndLookup(ctx, dir, name2, inode.Mode())
}

func FsFileOps(ctx context.Context, dir casfuse.Dir, name string) error {
	dentry := testDentry(dir.FS(), name)
	inode, err := dir.CreateFile(ctx, dentry, 0)
	if err != nil {
		return err
	}
	file, ok := inode.(casfuse.File)
	if !ok {
		return fmt.Errorf("expected file object didn't get one")
	}

	if err := file.Open(ctx, nil, true, false); err != nil {
		return err
	}

	if err := file.Fsync(ctx); err != nil {
		return err
	}
	testString := "Test data to write to file"
	if n, err := file.WriteAt([]byte(testString), int64(len(testString))); err != nil {
		return err
	} else {
		if n != len(testString) {
			return fmt.Errorf("unexpected write size %d (%d)", n, len(testString))
		}
	}
	if err := file.Flush(ctx); err != nil {
		return err
	}
	testBack := make([]byte, len(testString))

	if n, err := file.ReadAt(testBack, 0); err != nil {
		return err
	} else if n != len(testString) {
		return fmt.Errorf("unexpected read size %d (%d)", n, len(testString))
	}

	file.Poll(ctx)
	if err := file.Release(ctx); err != nil {
		return err
	}
	return nil
}
