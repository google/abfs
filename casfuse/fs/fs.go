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
	"fmt"
	"io/fs"
	"os"
	"sync/atomic"
	"time"

	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse"
)

type FS struct {
	rDev       uint32
	uid        uint32
	gid        uint32
	blockSize  uint32
	blocks     uint64
	inode      atomic.Uint64
	ext        map[casfuse.ExtType]interface{}
	ready      bool
	hasher     hash.Hasher
	rootDentry casfuse.Dentry
	dcache     casfuse.Dcache
}

func NewFS(rDev, uid, gid uint32) *FS {
	fs := &FS{
		rDev:      rDev,
		uid:       uid,
		gid:       gid,
		blockSize: uint32(os.Getpagesize()),
		ext:       make(map[casfuse.ExtType]interface{}),
		dcache:    casfuse.CreateDcache(),
	}

	fs.inode.Store(1)
	return fs
}
func (f *FS) Dcache() casfuse.Dcache {
	return f.dcache
}
func (f *FS) SetReady(val bool) {
	f.ready = val
}
func (f *FS) Ready() bool {
	return f.ready
}
func (f *FS) Hasher() hash.Hasher {
	return f.hasher
}
func (f *FS) SetHasher(h hash.Hasher) {
	f.hasher = h
}
func (f *FS) createInode(size int64, mode os.FileMode) *inode {
	ts := time.Now()
	return &inode{
		iNo:    f.inode.Add(1),
		nLinks: 1,
		mode:   mode,
		cTime:  ts,
		mTime:  ts,
		aTime:  ts,
		size:   size,
		fs:     f,
	}
}

func (f *FS) CreateRootDir() {
	f.SetRoot(NewRootDir(f))

}

func (f *FS) SetRoot(node casfuse.Inode) (err error) {

	dentry := f.dcache.Lookup(nil, "")
	if dentry.Inode() != nil {
		return fmt.Errorf("root dentry already exists for file system")
	}
	dentry.Instantiate(node)
	f.rootDentry = dentry
	return nil
}
func (f *FS) Root() casfuse.Dentry {
	return f.rootDentry
}
func (f *FS) newBaseNode(parent FsDir, mode fs.FileMode) *baseNode {
	return &baseNode{
		inode:  *f.createInode(0, mode),
		parent: parent,
	}

}
func (f *FS) RegisterExt(key casfuse.ExtType, any interface{}) {
	f.ext[key] = any
}
func (f *FS) GetExt(key casfuse.ExtType) interface{} {
	return f.ext[key]
}
