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

package fscas

import (
	"context"
	"io"
	"sync/atomic"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
)

/*
 * CAS directory
 *   Extend fs.Dir to include CAS attributes track both
 * the directory hash which is the full contents hash of the
 * directory as well as the directory listing which only contains
 * the names of the entries within the diretory.
 *
 *  Snapshotting:
 *    snapshot_dir.go  -- similar implemenation of kmod version
 *        - Snapshot() -- snapshot the full directory tree data
 *        - SnapshotDirList() -- snapshot the file entries of the directory
 *
 *  Deeptree:
 *    deeptree.go -- populate a directory and sub directories based
 *     on the deeptree hash.
 *
 */
type dir struct {
	fs.Dir
	node
	extType          cas.DirObjectExtType
	commitHash       hash.Hash
	validDirListHash atomic.Bool
	dirListHash      hash.Hash
}

func (d *dir) createNewCASNode(e *cas.DirEntry) (fs.Node, error) {
	size := e.Size
	mode := e.Mode

	// Zero out directory size
	if mode.IsDir() {
		size = 0
	}
	node, err := d.CreateNewLocked(e.Name, e.Mode)
	if err != nil {
		return nil, err
	}
	casNode := node.(Node)

	if e.ExtType == cas.DirObjectExtTypeSubModule {
		// Set commit hash
		d.commitHash = e.Hash
	} else {
		casNode.InitHash(e.Hash)
	}
	node.SetSize(size)
	return node, nil
}

func (d *dir) load(ctx context.Context) (err error) {
	cas := GetStore(d.FS())
	if d.hash.IsEmptyDir() {
		d.SetLoaded()
		return nil
	}
	reader, err := cas.GetDirEntries(d.hash)
	if err != nil {
		return err
	}
	defer reader.Close()
	for {
		e, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				err = nil
				break
			} else {
				return err
			}
		}
		_, err = d.createNewCASNode(e)
		if err != nil {
			return err
		}
	}

	d.SetLoaded()
	return nil

}

func (d *dir) ensureLoaded(ctx context.Context) error {
	d.Lock()
	defer d.Unlock()
	if d.IsLoaded() {
		return nil
	}
	return d.load(ctx)
}
func (d *dir) Empty(ctx context.Context) (bool, error) {
	if err := d.ensureLoaded(ctx); err != nil {
		return false, err
	}
	return d.Dir.Empty(ctx)

}
func (d *dir) Entries(ctx context.Context) (map[string]casfuse.Inode, error) {
	if err := d.ensureLoaded(ctx); err != nil {
		return nil, err
	}
	return d.Dir.Entries(ctx)
}
func (d *dir) Lookup(ctx context.Context, dentry casfuse.Dentry) (casfuse.Inode, error) {
	if err := d.ensureLoaded(ctx); err != nil {
		return nil, err
	}
	return d.Dir.Lookup(ctx, dentry)

}
func (d *dir) CreateFile(ctx context.Context, dentry casfuse.Dentry, mode cas.FileMode) (inode casfuse.Inode, err error) {
	defer func() {
		if err == nil {
			d.changed()
		}
	}()
	err = d.ensureLoaded(ctx)
	if err != nil {
		return inode, err
	}
	inode, err = d.Dir.CreateFile(ctx, dentry, mode)
	return inode, err
}
func (d *dir) Mkdir(ctx context.Context, dentry casfuse.Dentry, perm cas.FileMode) (inode casfuse.Inode, err error) {
	defer func() {
		if err == nil {
			d.changed()
		}
	}()
	err = d.ensureLoaded(ctx)
	if err != nil {
		return inode, err
	}
	inode, err = d.Dir.Mkdir(ctx, dentry, perm)
	return inode, err
}
func (d *dir) Remove(ctx context.Context, dentry casfuse.Dentry) (err error) {
	defer func() {
		if err == nil {
			d.changed()
		}
	}()
	err = d.ensureLoaded(ctx)
	if err != nil {
		return err
	}
	err = d.Dir.Remove(ctx, dentry)
	return err
}
func (d *dir) Symlink(ctx context.Context, dentry casfuse.Dentry, target string) (inode casfuse.Inode, err error) {
	defer func() {
		if err == nil {
			d.changed()
		}
	}()
	err = d.ensureLoaded(ctx)
	if err != nil {
		return inode, err
	}
	inode, err = d.Dir.Symlink(ctx, dentry, target)
	return inode, err
}
func (d *dir) Link(ctx context.Context, source casfuse.Dentry, dentry casfuse.Dentry) (inode casfuse.Inode, err error) {
	defer func() {
		if err == nil {
			d.changed()
		}
	}()
	err = d.ensureLoaded(ctx)
	if err != nil {
		return inode, err
	}
	inode, err = d.Dir.Link(ctx, source, dentry)
	return inode, err
}
func (d *dir) Rename(ctx context.Context, oldDentry casfuse.Dentry, newDirInode casfuse.Inode, newDentry casfuse.Dentry) (err error) {
	defer func() {
		if err == nil {
			d.changed()
			newDirInode.(*dir).changed() // dosen't matter if same directory (need to check if non cas dir is used though)
		}

	}()
	err = d.ensureLoaded(ctx)
	if err != nil {
		return err
	}
	err = d.Dir.Rename(ctx, oldDentry, newDirInode, newDentry)
	return err
}
func (d *dir) Mknod(ctx context.Context, dentry casfuse.Dentry, mode cas.FileMode, rDev int) (inode casfuse.Inode, err error) {
	defer func() {
		if err == nil {
			d.changed()
		}
	}()
	err = d.ensureLoaded(ctx)
	if err != nil {
		return inode, err
	}
	inode, err = d.Dir.Mknod(ctx, dentry, mode, rDev)
	return inode, err
}

// Changed is called when a directory level entry has changed
func (d *dir) changed() {
	d.childChanged()

	// This matches kmod but could be problematic if
	// dir list changes during snapshot.
	// TODO: convert both to use same functionality as
	// dirty count
	d.validDirListHash.Store(false)
}

// child change indicates that a node or sub node has changed and the
// hash is no longer valid
func (d *dir) childChanged() {
	var cur *dir = d

	// TODO: This is done without locks, could be problem if a parent
	// node relationship is changed, during walk.(same problem in kmod)
	for cur != nil {
		cur.MakeDirty()
		if cur.Parent() != nil {
			if par, ok := cur.Parent().(*dir); ok {
				cur = par
			} else {
				cur = nil
			}
		} else {
			cur = nil
		}

	}

}
func (d *dir) SetExcluded(val bool) {
	changed := false

	d.Lock()
	defer func() {
		if changed {
			d.isExcluded = val
		}
		d.Unlock()
		if changed {
			d.childChanged()
		}
	}()
	changed = val != d.isExcluded
}

// Force hash value
func (d *dir) ForceHashAndSize(hash hash.Hash, _ int64) error {
	changed := false
	d.Lock()
	defer func() {
		d.Unlock()
		if changed {
			if par, ok := d.Parent().(*dir); ok {
				par.childChanged()
			}

		}
	}()

	if d.IsDirty() {
		return casfuse.ErrDirty
	}
	if hash.Equals(d.hash) {
		return nil
	}
	if d.isLoaded && (!d.hash.IsEmptyDir()) {
		return casfuse.ErrDirNotEmpty
	}
	d.hash = hash
	d.isLoaded = false
	d.validDirListHash.Store(false)
	d.dirtiness.Store(0)
	changed = true
	return nil
}

func (d *dir) CreateFileNode(t cas.FileMode) (fs.Node, error) {
	return CreateFile(d, t), nil
}
func (d *dir) CreateDirNode(t cas.FileMode) (fs.Node, error) {
	return CreateDir(d, t), nil
}
func (d *dir) CreateSymLinkNode(t cas.FileMode) (fs.Node, error) {
	return CreateSymLink(d, t), nil
}
func (d *dir) CreateFifoNode(t cas.FileMode) (fs.Node, error) {
	return fs.NewFifo(d, t), nil
}
func (d *dir) CreateSocketNode(t cas.FileMode) (fs.Node, error) {
	return fs.NewSocket(d, t), nil
}

func (d *dir) ForceLoadAll(ctx context.Context) error {
	d.Lock()
	defer d.Unlock()
	if !d.IsLoaded() {
		if err := d.load(ctx); err != nil {
			return err
		}
		for _, e := range *d.Dir.InternalEntriesLocked() {
			if e.Mode().IsDir() {
				if dn, ok := e.(fs.FsDir); ok {
					if err := dn.ForceLoadAll(ctx); err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}
func (d *dir) AttrAudit(ctx context.Context, _ casfuse.Dentry) {
	// Don't tap dir stats
}
func (d *dir) MkdirAudit(ctx context.Context, dentry casfuse.Dentry, inode casfuse.Inode) {
	GetTap(d.FS()).Mkdir(ctx, dentry, inode.(*dir))
}
func (d *dir) CreateFileAudit(ctx context.Context, dentry casfuse.Dentry, inode casfuse.Inode) {

}
func (d *dir) SymlinkAudit(ctx context.Context, dentry casfuse.Dentry, inode casfuse.Inode) {
	GetTap(d.FS()).Symlink(ctx, dentry, inode.(*symLink))
}
func (d *dir) UnlinkAudit(ctx context.Context, dentry casfuse.Dentry, inode casfuse.Inode) {
	if n, ok := inode.(Node); ok {
		GetTap(d.FS()).Unlink(ctx, dentry, n)
	}

}
func (d *dir) RenameAudit(ctx context.Context, info *fs.RenameAuditInfo) {
	var node, replaceNode Node
	if info.ReplacedNode != nil {
		if n, ok := info.ReplacedNode.(Node); ok {
			replaceNode = n
		}
	}
	if n, ok := info.Node.(Node); ok {
		node = n
	}

	GetTap(d.FS()).Rename(ctx, d, info.OldDentry, info.NewDir.(*dir), info.NewDentry, node, replaceNode)
}
func (d *dir) LinkAudit(ctx context.Context, source, dentry casfuse.Dentry) {
	if n, ok := source.Inode().(Node); ok {
		GetTap(d.FS()).Link(ctx, n, source, dentry)
	}
}
