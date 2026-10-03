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

package tap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"sync"

	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse"
)

const TapSeqVersion = 0x02

type Session struct {
	lock     *sync.Mutex
	wg       *sync.WaitGroup
	buf      *bytes.Buffer
	w        *bufio.Writer
	finished bool
	sid      int
	hasher   hash.Hasher
}

func (s *Session) Size() int64 {
	return int64(s.buf.Len())
}

func (s *Session) Read(p []byte) (n int, err error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.buf.Read(p)
}

func (s *Session) addLocked(ent *fs.TapFSEntry) {
	// Writes to bytes.Buffer won't fail
	_ = s.w.WriteByte(byte(ent.Type))
	var dirty byte = 0
	if ent.Dirty {
		dirty = 1
	}
	_ = s.w.WriteByte(dirty)

	_, _ = ent.Hash.Write(s.w)
	mode := fs.FSModeToOSMode(ent.Mode)
	_ = binary.Write(s.w, binary.NativeEndian, ent.Size)
	_ = binary.Write(s.w, binary.NativeEndian, mode)
	_, _ = s.w.WriteString(ent.Path)
	_ = s.w.WriteByte(0x00)
	if len(ent.AuxPath) > 0 {
		_, _ = s.w.WriteString(ent.AuxPath)
	} else {
		_, _ = s.w.WriteString(ent.AuxPath)
	}
	_ = s.w.WriteByte(0x00)
	_ = s.w.Flush()
}
func (s *Session) add(ent *fs.TapFSEntry) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.addLocked(ent)
}

func (s *Session) simple(node TapNode, dentry casfuse.Dentry, tapType fs.TapType, auxDentry casfuse.Dentry) {
	out := &fs.TapFSEntry{
		Type:  tapType,
		Dirty: node.IsDirty(),
		Path:  dentry.Path(),
		Mode:  node.Mode()}

	if auxDentry != nil {
		out.AuxPath = auxDentry.Path()
	}
	if out.Dirty {
		out.Hash = s.hasher.ZeroHash()
	} else {
		out.Hash = node.Hash()
		out.Size = node.Size()

	}
	s.add(out)

}
func (s *Session) Stat(node TapNode, dentry casfuse.Dentry) {
	s.simple(node, dentry, fs.TapFSStat, nil)

}

func (s *Session) Lsdir(ctx context.Context, dentry casfuse.Dentry, dir TapDir) {
	h, _ := dir.SnapshotDirList(ctx)
	s.add(&fs.TapFSEntry{
		Type:  fs.TapFSReadDir,
		Dirty: false,
		Path:  dentry.Path(),
		Mode:  dir.Mode(),
		Hash:  h,
	})

}
func (s *Session) ReadLink(node TapNode, dentry casfuse.Dentry) {
	s.simple(node, dentry, fs.TapFSSymlinkRead, nil)
}

func (s *Session) Symlink(node TapNode, dentry casfuse.Dentry) {
	s.simple(node, dentry, fs.TapFSSymlink, nil)

}
func (s *Session) Mkdir(node TapNode, dentry casfuse.Dentry) {
	s.simple(node, dentry, fs.TapFSMkdir, nil)
}

func (s *Session) Unlink(node TapNode, dentry casfuse.Dentry) {
	s.simple(node, dentry, fs.TapFSUnlink, nil)

}

func (s *Session) AttrChanged(node TapNode, dentry casfuse.Dentry) {
	s.simple(node, dentry, fs.TapFSAttrChange, nil)

}

func (s *Session) Open(node TapNode, dentry casfuse.Dentry, isReadOnly bool) {
	evt := fs.TapFSOpenWrite
	if isReadOnly {
		evt = fs.TapFSOpenRead
	}
	s.simple(node, dentry, evt, nil)
}

func (s *Session) Rename(oldDir TapDir, oldDentry casfuse.Dentry, newDir TapDir, newDentry casfuse.Dentry, node, replacedNode TapNode) {
	s.simple(node, newDentry, fs.TapFSRename, oldDentry)
}

func (s *Session) Link(ctx context.Context, node TapNode, source, dentry casfuse.Dentry) {
	s.simple(node, dentry, fs.TapFSLink, source)
}
