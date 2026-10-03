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

package fs

import (
	"context"
	"io/fs"
	"time"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse"
)

// Directory internal interface
type FsDir interface {
	casfuse.Dir
	Node

	AddChild(name string, node casfuse.Inode)
	Empty(ctx context.Context) (bool, error)
	InternalEntriesLocked() *map[string]Node
	ForceLoadAll(ctx context.Context) error
	fs() *FS
}

type NodeFactory interface {
	CreateDirNode(mode cas.FileMode) (Node, error)
	CreateFileNode(mode cas.FileMode) (Node, error)
	CreateSymLinkNode(mode cas.FileMode) (Node, error)
	CreateFifoNode(mode cas.FileMode) (Node, error)
	CreateSocketNode(mode cas.FileMode) (Node, error)
}

type RenameAuditInfo struct {
	Node         casfuse.Inode
	OldDentry    casfuse.Dentry
	NewDir       FsDir
	NewDentry    casfuse.Dentry
	ReplacedNode casfuse.Inode // If overwritting a node
}

// Audit interface is called while holding the directory locks
type DirAudit interface {
	MkdirAudit(context.Context, casfuse.Dentry, casfuse.Inode)
	CreateFileAudit(context.Context, casfuse.Dentry, casfuse.Inode)
	SymlinkAudit(context.Context, casfuse.Dentry, casfuse.Inode)
	UnlinkAudit(context.Context, casfuse.Dentry, casfuse.Inode)
	RenameAudit(context.Context, *RenameAuditInfo)
	AttrAudit(context.Context, casfuse.Dentry)
	LinkAudit(context.Context, casfuse.Dentry, casfuse.Dentry)
}

type Dir struct {
	baseNode
	nf      NodeFactory
	audit   DirAudit
	entries map[string]Node
}

func (d *Dir) SetNodeFactory(nf NodeFactory) {
	d.nf = nf
}
func (d *Dir) SetAudit(a DirAudit) {
	d.audit = a
}
func (d *Dir) Entries(ctx context.Context) (map[string]casfuse.Inode, error) {
	d.lock.Lock()
	defer d.lock.Unlock()
	out := make(map[string]casfuse.Inode, len(d.entries))
	for k, e := range d.entries {
		out[k] = e
	}

	return out, nil
}

func (d *Dir) Lookup(ctx context.Context, dentry casfuse.Dentry) (casfuse.Inode, error) {

	d.lock.Lock()
	defer d.lock.Unlock()

	node, ok := d.entries[dentry.Name()]
	if !ok {
		return nil, casfuse.ErrEntryNotFound
	}

	return node, nil

}
func (d *Dir) CreateTmpFile(ctx context.Context, dentry casfuse.Dentry, mode cas.FileMode) (casfuse.Inode, error) {
	return nil, casfuse.ErrNotSupported
}
func (d *Dir) CreateFile(ctx context.Context, dentry casfuse.Dentry, mode cas.FileMode) (casfuse.Inode, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

	node, ok := d.entries[dentry.Name()]
	if ok {
		if _, ok := node.(casfuse.File); ok {
			return node, nil
		} else {
			return nil, casfuse.ErrNotAFile
		}
	}
	out, err := d.CreateNewLocked(dentry.Name(), mode)
	if err != nil {
		return nil, err
	}
	d.audit.CreateFileAudit(ctx, dentry, out)
	return out, nil

}
func (d *Dir) Mkdir(ctx context.Context, dentry casfuse.Dentry, perm cas.FileMode) (casfuse.Inode, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

	node, ok := d.entries[dentry.Name()]
	if ok {
		if node.Mode().IsDir() {
			return nil, casfuse.ErrEntryExists
		}
		return nil, casfuse.ErrNotADirectory
	}
	// Create directory object
	mode := fs.ModeDir | perm.Perm()
	out, err := d.CreateNewLocked(dentry.Name(), cas.FileMode(mode))
	if err != nil {
		return nil, err
	}
	d.audit.MkdirAudit(ctx, dentry, out)
	return out, err
}

func (d *Dir) Empty(ctx context.Context) (bool, error) {
	d.lock.Lock()
	defer d.lock.Unlock()
	return len(d.entries) == 0, nil
}
func (d *Dir) Remove(ctx context.Context, dentry casfuse.Dentry) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	node, ok := d.entries[dentry.Name()]
	if !ok {
		return casfuse.ErrEntryNotFound
	}
	if node.Mode().IsDir() {
		delDir := node.(FsDir)
		empty, err := delDir.Empty(ctx)
		if err != nil {
			return err
		}
		if !empty {
			return casfuse.ErrDirNotEmpty
		}
	}

	d.removeChild(dentry.Name())
	ts := time.Now()
	d.SetCtime(ts)
	d.SetMtime(ts)
	node.SetCtime(ts)

	d.audit.UnlinkAudit(ctx, dentry, node)
	node.DecNlinks()
	return nil
}
func (d *Dir) Symlink(ctx context.Context, dentry casfuse.Dentry, target string) (casfuse.Inode, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

	if _, ok := d.entries[dentry.Name()]; ok {
		return nil, casfuse.ErrEntryExists
	}
	// Create symlink
	node, err := d.CreateNewLocked(dentry.Name(), cas.FileModeSymLink())
	if err != nil {
		return nil, err
	}
	node.(casfuse.SymLink).SetTarget(ctx, target)
	d.audit.SymlinkAudit(ctx, dentry, node)
	return node, nil
}

func renameLock(oldDir, newDir FsDir) {
	if oldDir.Ino() == newDir.Ino() {
		oldDir.Lock()
	} else if oldDir.Ino() < newDir.Ino() {
		oldDir.Lock()
		newDir.Lock()
	} else {
		newDir.Lock()
		oldDir.Lock()
	}
}
func renameUnlock(oldDir, newDir FsDir) {
	oldDir.Unlock()
	if oldDir.Ino() != newDir.Ino() {
		newDir.Unlock()
	}
}
func (d *Dir) Rename(ctx context.Context, oldDentry casfuse.Dentry, newDirInode casfuse.Inode, newDentry casfuse.Dentry) error {

	newDir := newDirInode.(FsDir)
	renameLock(d, newDir)
	defer renameUnlock(d, newDir)

	node, ok := d.entries[oldDentry.Name()]
	if !ok {
		return casfuse.ErrEntryNotFound
	}

	oldNode, ok := (*newDir.InternalEntriesLocked())[newDentry.Name()]
	if !ok {
		oldNode = nil
	}

	info := &RenameAuditInfo{
		Node:         node,
		OldDentry:    oldDentry,
		NewDir:       newDir,
		NewDentry:    newDentry,
		ReplacedNode: oldNode,
	}

	d.audit.RenameAudit(ctx, info)

	ts := time.Now()
	d.removeChild(oldDentry.Name())
	d.SetMtime(ts)
	d.SetCtime(ts)
	newDir.AddChild(newDentry.Name(), node)

	return nil
}

func (d *Dir) Mknod(ctx context.Context, dentry casfuse.Dentry, mode cas.FileMode, rDev int) (casfuse.Inode, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

	if _, ok := d.entries[dentry.Name()]; ok {
		return nil, casfuse.ErrEntryExists
	}
	out, err := d.CreateNewLocked(dentry.Name(), mode)
	if err != nil {
		return nil, err
	}
	return out, nil

}

func (d *Dir) Link(ctx context.Context, source casfuse.Dentry, dentry casfuse.Dentry) (casfuse.Inode, error) {
	if source.Inode() == nil {
		return nil, casfuse.ErrNotAFile
	}
	d.lock.Lock()
	defer d.lock.Unlock()
	if _, ok := d.entries[dentry.Name()]; ok {
		return nil, casfuse.ErrEntryExists
	}
	node := source.Inode().(Node)
	node.IncNlinks()

	d.entries[dentry.Name()] = node

	ts := time.Now()
	d.SetCtime(ts)
	d.SetMtime(ts)
	d.audit.LinkAudit(ctx, source, dentry)
	return node, nil

}
func (d *Dir) AddChild(name string, inode casfuse.Inode) {
	node := inode.(Node)
	d.entries[name] = node

	ts := time.Now()
	d.SetCtime(ts)
	d.SetMtime(ts)

}
func (d *Dir) removeChild(name string) {
	delete(d.entries, name)
}

// Allow extended objects to create nodes in locked state
func (d *Dir) CreateNewLocked(name string, mode cas.FileMode) (Node, error) {
	if d.nf == nil {
		return nil, casfuse.ErrNotSupported
	}
	var newNode Node
	var err error
	if mode.IsRegular() {
		newNode, err = d.nf.CreateFileNode(mode)
	}
	if mode.IsDir() {
		newNode, err = d.nf.CreateDirNode(mode)
	}
	if mode&fs.ModeSymlink != 0 {
		newNode, err = d.nf.CreateSymLinkNode(mode)
	}
	if mode&fs.ModeNamedPipe != 0 {
		newNode, err = d.nf.CreateFifoNode(mode)
	}
	if mode&fs.ModeSocket != 0 {
		newNode, err = d.nf.CreateSocketNode(mode)
	}
	if newNode == nil {
		if err == nil {
			err = casfuse.ErrNotSupported
		}
		return nil, err
	}
	d.AddChild(name, newNode)
	return newNode, nil
}

func (d *Dir) InternalEntriesLocked() *map[string]Node {
	return &d.entries
}
func (d *Dir) Attr(ctx context.Context, dentry casfuse.Dentry) *casfuse.NodeAttr {
	d.audit.AttrAudit(ctx, dentry)
	return d.getAttr()
}
func (d *Dir) ForceLoadAll(ctx context.Context) error {
	return nil
}
