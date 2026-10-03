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

package casfuse

import (
	"context"
	"fmt"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/fs"
)

const MaxPathLen = 4096

// Provide cas.FileInfo interface
type FileInfo struct {
	name string
	attr *NodeAttr
}

func (n *FileInfo) Name() string {
	return n.name
}
func (n *FileInfo) Size() int64 {
	return n.attr.Size
}
func (n *FileInfo) Mode() cas.FileMode {
	return n.attr.Mode
}
func (n *FileInfo) ModTime() time.Time {
	return n.attr.Mtime
}
func (n *FileInfo) IsDir() bool {
	return n.attr.Mode.IsDir()
}
func (n *FileInfo) Sys() any {
	return nil
}
func DentryHash(dir Dentry, name string) string {
	// Combine parent and name as key
	return fmt.Sprintf("%p-%s", dir, name)
}

func CreateDcache() Dcache {
	return &dcache{entries: make(map[string]Dentry)}
}

type DentryState uint32

const (
	DentryStateHashed DentryState = 1 << iota
)

type dentry struct {
	name   string
	parent Dentry
	inode  Inode
	state  atomic.Uint32
	count  atomic.Int32
	dcache Dcache
	ext    any
}

func (d *dentry) Dcache() Dcache {
	return d.dcache
}
func (d *dentry) Ext() any {
	return d.ext
}
func (d *dentry) SetExt(a any) {
	d.ext = a
}
func (d *dentry) Inode() Inode {
	return d.inode
}
func (d *dentry) Instantiate(inode Inode) {
	d.inode = inode
	// Notify file system of change in dentry
	d.dcache.NotifyChange(d)

}
func (d *dentry) Name() string {
	return d.name
}
func (d *dentry) Parent() Dentry {
	return d.parent
}
func (d *dentry) Path() string {
	return d.dcache.Path(d)
}
func (d *dentry) Get() {
	d.count.Add(1)
}
func (d *dentry) Put() {
	val := d.count.Add(-1)
	if val == 0 {
		// Cleanup inode
	}
}
func (d *dentry) IsHashed() bool {
	return (d.state.Load()&uint32(DentryStateHashed) != 0)
}

// Because we support creating entities interally as well as through the kernel
// we need to handle the dcache at the application layer.  The fuse bindings
// return a dentry to that the kernel will cache.  When making calls from the
// kernel it is possible that the dentry has been removed from the application
// layer cache (before the application has had a chance to notify the kernel).
// In these cases the dentry object and underlying inode object still reside
// in memory.
//

type dcache struct {
	entries map[string]Dentry
	lock    sync.RWMutex
	watcher DcacheWatch
}

// Create a new dentry
func (dc *dcache) Create(dir Dentry, name string) Dentry {
	out := &dentry{name: name, parent: dir, dcache: dc}
	return out
}

func (dc *dcache) lookupLocked(dir Dentry, name string) Dentry {
	hash := DentryHash(dir, name)
	if out, ok := dc.entries[hash]; ok {
		return out
	}
	out := dc.Create(dir, name)
	out.(*dentry).state.Or(uint32(DentryStateHashed))
	dc.entries[hash] = out
	return out
}

// Lookup a given name with parent directory in cache, add negative
// dentry if not found.
func (dc *dcache) Lookup(dir Dentry, name string) Dentry {
	dc.lock.Lock()
	defer dc.lock.Unlock()
	return dc.lookupLocked(dir, name)
}

// Add an entry to the dcache
func (dc *dcache) Add(d Dentry) error {
	hash := DentryHash(d.Parent(), d.Name())
	dc.lock.Lock()
	defer dc.lock.Unlock()
	if _, ok := dc.entries[hash]; !ok {
		dc.entries[hash] = d
		d.(*dentry).state.Or(uint32(DentryStateHashed))
		return nil
	} else {
		return ErrEntryExists
	}
}

// Remove an entry from dcache, after removing mark the
// dentry as not hashed
func (dc *dcache) Remove(d Dentry) {
	hash := DentryHash(d.Parent(), d.Name())
	dc.lock.Lock()
	defer dc.lock.Unlock()
	delete(dc.entries, hash)
	d.(*dentry).state.And(^uint32(DentryStateHashed))
}
func (dc *dcache) Rename(oldEntry, newEntry Dentry) {
	oldHash := DentryHash(oldEntry.Parent(), oldEntry.Name())
	newHash := DentryHash(newEntry.Parent(), newEntry.Name())
	dc.lock.Lock()
	defer dc.lock.Unlock()
	delete(dc.entries, oldHash)

	oldEntry.(*dentry).parent = newEntry.Parent()
	oldEntry.(*dentry).name = newEntry.Name()
	newEntry.Instantiate(oldEntry.Inode())
	// Check if new hash exists if so remove it
	if n, ok := dc.entries[newHash]; ok {
		n.(*dentry).state.And(^uint32(DentryStateHashed))
	}

	dc.entries[newHash] = oldEntry
	newEntry.(*dentry).state.Or(uint32(DentryStateHashed))
}

// Backtrack the path to build a string.
func (dc *dcache) Path(d Dentry) string {
	result := make([]byte, 0, MaxPathLen)
	dc.lock.Lock()
	defer dc.lock.Unlock()

	for d != nil {
		result = append([]byte(fmt.Sprintf("/%s", d.Name())), result...)
		d = d.Parent()
	}
	return path.Clean(string(result))
}

func (dc *dcache) RegisterWatch(w DcacheWatch) {
	dc.watcher = w
}

func (dc *dcache) NotifyChange(d Dentry) {
	if dc.watcher != nil {
		dc.watcher.Change(d)
	}
}
func create(dir Dentry, name string) Dentry {
	return dir.Dcache().Create(dir, name)
}

func unregister(dentry Dentry) {
	dentry.Dcache().Remove(dentry)
}
func Lookup(ctx context.Context, dir Dentry, name string) Dentry {
	// Check if entry is in cache, this will add a negative
	// dentry if not
	out := dir.Dcache().Lookup(dir, name)
	if out.Inode() != nil {
		return out
	}
	inode, err := dir.Inode().(Dir).Lookup(ctx, out)
	if err == nil {
		out.Instantiate(inode)
	}
	return out
}
func LookupNegative(ctx context.Context, dir Dentry, name string) (Dentry, error) {
	out := Lookup(ctx, dir, name)
	if out.Inode() == nil {
		return out, nil
	}
	return nil, ErrEntryExists
}
func LookupPositive(ctx context.Context, dir Dentry, name string) (Dentry, error) {
	out := Lookup(ctx, dir, name)
	if out.Inode() != nil {
		return out, nil
	}
	return nil, ErrEntryNotFound
}
func RenameDentries(oldEntry, newEntry Dentry) {
	oldEntry.Dcache().Rename(oldEntry, newEntry)
}
func Resolve(ctx context.Context, d Dentry, p string) (Dentry, error) {

	p = path.Clean(p)
	for i := 0; ; {
		if i >= len(p) || p[i:] == "." {
			return d, nil
		}
		s := cas.IndexSlashAfter(p, i)
		name := p[i:s]

		child := Lookup(ctx, d, name)

		if child.Inode() == nil {
			return nil, ErrEntryNotFound
		}
		if !child.IsHashed() {
			return nil, ErrEntryNotFound
		}
		if cas.FileModeIsSymLink(child.Inode().Mode()) {
			target, err := child.Inode().(SymLink).Target(ctx, child)
			if err != nil {
				// join target
				return nil, err
			}
			p = path.Join(p[:i], path.Clean(target), p[s:])
			i = 0
		} else if child.Inode().Mode().IsDir() {
			i = s + 1
			d = child
		} else {
			if s < len(p) {
				return nil, ErrEntryNotFound
			}
			return child, nil
		}
	}

}
func Symlink(ctx context.Context, dir Dentry, name, target string) (Dentry, error) {
	child, err := LookupNegative(ctx, dir, name)
	if err != nil {
		return nil, err
	}
	inode, err := dir.Inode().(Dir).Symlink(ctx, child, target)
	if err != nil {
		return nil, err
	}
	child.Instantiate(inode)
	return child, nil

}
func Mkdir(ctx context.Context, dir Dentry, name string, mode cas.FileMode) (Dentry, error) {
	child, err := LookupNegative(ctx, dir, name)
	if err != nil {
		return nil, err
	}
	inode, err := dir.Inode().(Dir).Mkdir(ctx, child, mode)
	if err != nil {
		return nil, err
	}
	child.Instantiate(inode)
	return child, nil
}
func Rename(ctx context.Context, oldDir Dentry, oldName string, newDir Dentry, newName string) error {
	source, err := LookupPositive(ctx, oldDir, oldName)
	if err != nil {
		return err
	}
	// This should probably all be done with the dcache lock held
	newEntry := create(newDir, newName)
	err = oldDir.Inode().(Dir).Rename(ctx, source, newDir.Inode(), newEntry)
	if err != nil {
		return err
	}

	RenameDentries(source, newEntry)
	return nil

}
func Mknod(ctx context.Context, dir Dentry, name string, mode cas.FileMode, rDev int) (Dentry, error) {
	child, err := LookupNegative(ctx, dir, name)
	if err != nil {
		return nil, err
	}
	inode, err := dir.Inode().(Dir).Mknod(ctx, child, mode, rDev)
	if err != nil {
		return nil, err
	}
	child.Instantiate(inode)
	return child, nil

}
func Remove(ctx context.Context, dir Dentry, name string) error {
	child, err := LookupPositive(ctx, dir, name)
	if err != nil {
		return err
	}
	err = dir.Inode().(Dir).Remove(ctx, child)
	if err != nil {
		return err
	}
	unregister(child)
	return nil
}
func Link(ctx context.Context, dir Dentry, name string, target Dentry) (Dentry, error) {
	child, err := LookupNegative(ctx, dir, name)
	if err != nil {
		return nil, err
	}
	inode, err := dir.Inode().(Dir).Link(ctx, target, child)
	if err != nil {
		return nil, err
	}
	child.Instantiate(inode)
	return child, nil

}
func Lstat(ctx context.Context, dir Dentry, name string) (cas.FileInfo, error) {
	dentry, err := LookupPositive(ctx, dir, name)
	if err != nil {
		return nil, err
	}
	attr := dentry.Inode().Attr(ctx, dentry)
	return &FileInfo{name: name, attr: attr}, nil
}
func Chmod(ctx context.Context, dir Dentry, name string, mode cas.FileMode) error {
	return nil
}
func Readlink(ctx context.Context, dir Dentry, name string) (string, error) {
	dentry, err := LookupPositive(ctx, dir, name)
	if err != nil {
		return "", err
	}
	if sym, ok := dentry.Inode().(SymLink); ok {
		return sym.Target(ctx, dentry)
	} else {
		return "", ErrEntryNotFound
	}
}
func CreateFile(ctx context.Context, dir Dentry, name string, mode cas.FileMode) (Dentry, error) {
	dentry := Lookup(ctx, dir, name)
	inode, err := dir.Inode().(Dir).CreateFile(ctx, dentry, mode)
	if err != nil {
		return nil, err
	}
	dentry.Instantiate(inode)
	return dentry, nil

}
func CreateTmpFile(ctx context.Context, dir Dentry, mode cas.FileMode) (Dentry, error) {
	dentry := create(dir, "")
	inode, err := dir.Inode().(Dir).CreateTmpFile(ctx, dentry, mode)
	if err != nil {
		return nil, err
	}
	dentry.Instantiate(inode)
	return dentry, err
}

func OpenFile(ctx context.Context, dir Dentry, name string, flags int, perm cas.FileMode) (fs.FSFile, error) {
	create := ((flags & os.O_CREATE) != 0)
	truncate := ((flags & os.O_TRUNC) != 0)
	readOnly := ((flags & os.O_RDONLY) != 0)

	var child Dentry
	var err error
	if create {
		child, err = CreateFile(ctx, dir, name, perm)
		if err != nil {
			return nil, err
		}
	} else {
		child = Lookup(ctx, dir, name)
		if _, ok := child.Inode().(File); !ok {
			err = ErrEntryNotFound
			return nil, err
		}
	}

	out := child.Inode().(File)
	err = out.Open(ctx, child, truncate, readOnly)
	if err != nil {
		return nil, err
	}
	return out, nil

}
