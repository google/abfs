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
	"sync"
	"time"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse"
)

type inode struct {
	iNo    uint64
	nLinks uint32
	mode   cas.FileMode
	cTime  time.Time
	mTime  time.Time
	aTime  time.Time
	size   int64
	fs     *FS
}

type Node interface {
	casfuse.Inode
	Lock()
	Unlock()
	SetCtime(t time.Time)
	SetAtime(t time.Time)
	SetSize(int64)
	IsDebug() bool
	SetDebug(val bool)
	IncNlinks()
	DecNlinks()
}
type baseNode struct {
	inode   inode
	lock    sync.Mutex
	debug   bool
	parent  FsDir
	noCache bool
}

func (b *baseNode) UpdateAtime() {
	b.inode.aTime = time.Now()
}
func (b *baseNode) UpdateMtime() {
	b.inode.mTime = time.Now()
}
func (b *baseNode) UpdateCandMtime() {
	ts := time.Now()
	b.inode.cTime = ts
	b.inode.mTime = ts
}
func (b *baseNode) SetMtime(t time.Time) {
	b.inode.mTime = t
}
func (b *baseNode) SetCtime(t time.Time) {
	b.inode.cTime = t
}
func (b *baseNode) SetAtime(t time.Time) {
	b.inode.aTime = t
}
func (b *baseNode) UpdateCtime() {
	b.inode.cTime = time.Now()
}
func (b *baseNode) SetSize(size int64) {
	b.inode.size = size
}

func (b *baseNode) Mode() cas.FileMode {
	return b.inode.mode
}
func (b *baseNode) Ino() uint64 {
	return b.inode.iNo
}
func (b *baseNode) Size() int64 {
	return b.inode.size
}

func (b *baseNode) getAttr() *casfuse.NodeAttr {
	n := &casfuse.NodeAttr{}
	b.lock.Lock()
	defer b.lock.Unlock()
	n.Ino = b.inode.iNo
	n.Size = b.inode.size
	n.Blocks = (uint64(b.inode.size) + uint64(b.inode.fs.blockSize) - 1) / uint64(b.inode.fs.blockSize)
	n.Ctime = b.inode.cTime
	n.Atime = b.inode.aTime
	n.Mtime = b.inode.mTime
	n.Mode = b.inode.mode
	n.Nlinks = b.inode.nLinks
	n.Uid = b.inode.fs.uid
	n.Gid = b.inode.fs.gid
	n.Rdev = b.inode.fs.rDev
	n.BlockSize = b.inode.fs.blockSize
	return n
}
func (b *baseNode) Lock() {
	b.lock.Lock()
}
func (b *baseNode) TryLock() bool {
	return b.lock.TryLock()
}
func (b *baseNode) Unlock() {
	b.lock.Unlock()
}
func (b *baseNode) FS() casfuse.FS {
	return b.inode.fs
}
func (b *baseNode) fs() *FS {
	return b.inode.fs
}
func (b *baseNode) Parent() FsDir {
	return b.parent
}

func (b *baseNode) CanCache() bool {
	return !b.noCache
}
func (b *baseNode) SetNoCache() {
	//	b.noCache = true
}

func (b *baseNode) IsDebug() bool {
	return b.debug
}
func (b *baseNode) SetDebug(val bool) {
	b.debug = val
}
func (b *baseNode) IncNlinks() {
	b.Lock()
	b.inode.nLinks++
	b.Unlock()
}
func (b *baseNode) DecNlinks() {
	b.Lock()
	b.inode.nLinks--
	b.Unlock()
}
