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
	"time"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
)

// NodeAttr stat attributes of a file/dir/fifo/...
type NodeAttr struct {
	Ino       uint64
	Size      int64
	Blocks    uint64
	Ctime     time.Time
	Atime     time.Time
	Mtime     time.Time
	Mode      cas.FileMode
	Nlinks    uint32
	Uid       uint32
	Gid       uint32
	Rdev      uint32
	BlockSize uint32
}

type contextKeyType struct{}

// ContextKey the context key is used by the fuse implementations to send caller data
// through the context
var ContextKey = &contextKeyType{}

type ContextValue struct {
	Pid uint32
}

// Extension types
type ExtType int

const (
	CASExt  ExtType = iota // Content Addressable
	GitExt                 // GIT
	CASTap                 // Tapping
	FuseExt                // Fuse control

)

type FS interface {
	Ready() bool
	SetReady(bool)

	Root() Dentry
	SetRoot(node Inode) error
	// Try to remove hasher here
	Hasher() hash.Hasher
	GetExt(key ExtType) interface{}
	RegisterExt(key ExtType, any interface{})
	Dcache() Dcache
}

// Inode interface for all supported nodes
type Inode interface {
	Mode() cas.FileMode
	Attr(ctx context.Context, dentry Dentry) *NodeAttr
	Ino() uint64
	Size() int64

	CanCache() bool

	FS() FS
}
type DcacheWatch interface {
	Change(Dentry)
}
type Dcache interface {
	Add(d Dentry) error
	Create(dir Dentry, name string) Dentry
	Lookup(dir Dentry, name string) Dentry
	Remove(d Dentry)
	Rename(oldEntry, newEntry Dentry)
	Path(d Dentry) string
	NotifyChange(d Dentry)
	RegisterWatch(DcacheWatch)
}
type Dentry interface {
	Name() string
	Parent() Dentry
	Path() string
	Instantiate(inode Inode)
	IsHashed() bool
	Inode() Inode
	Get()
	Put()
	Ext() any
	SetExt(any)
	Dcache() Dcache
}

// Dir directory interface
type Dir interface {
	Inode

	Entries(ctx context.Context) (map[string]Inode, error)
	Lookup(ctx context.Context, dentry Dentry) (Inode, error)
	Mkdir(ctx context.Context, dentry Dentry, perm cas.FileMode) (Inode, error)
	Remove(ctx context.Context, dentry Dentry) error
	CreateTmpFile(ctx context.Context, dentry Dentry, mode cas.FileMode) (Inode, error)
	CreateFile(ctx context.Context, dentry Dentry, mode cas.FileMode) (Inode, error)
	Symlink(ctx context.Context, dentry Dentry, target string) (Inode, error)
	Rename(ctx context.Context, oldDentry Dentry, newDirInode Inode, newDentry Dentry) error
	Mknod(ctx context.Context, dentry Dentry, mode cas.FileMode, rDev int) (Inode, error)
	Link(ctx context.Context, source Dentry, dentry Dentry) (Inode, error)
}

// File interface
type File interface {
	Inode
	SetAttr(ctx context.Context, dentry Dentry, size *int64, atime, mtime *time.Time, mode *cas.FileMode)
	Open(ctx context.Context, dentry Dentry, truncate, readOnly bool) error
	Close() error
	Fsync(ctx context.Context) error
	ReadAt(p []byte, off int64) (int, error)
	WriteAt(p []byte, off int64) (int, error)
	Release(ctx context.Context) error
	Flush(ctx context.Context) error
	Fallocate(ctx context.Context, mode int, offset, len int64) error
	Lseek(ctx context.Context, offset int64, whence uint32) (int64, error)
	Poll(ctx context.Context) (bool, bool)
}

// Symlink interface
type SymLink interface {
	Inode

	Target(ctx context.Context, dentry Dentry) (string, error)
	SetTarget(ctx context.Context, target string)
}

// Fifo interface
type Fifo interface {
	Inode
}

// Socket interface
type Socket interface {
	Inode
}
