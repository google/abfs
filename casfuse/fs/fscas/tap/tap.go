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
	"context"
	"io/fs"
	"sync"
	"syscall"

	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse"
)

type TapNode interface {
	Mode() fs.FileMode
	IsDirty() bool
	Size() int64
	Hash() hash.Hash
}
type TapDir interface {
	TapNode
	SnapshotDirList(ctx context.Context) (hash.Hash, error)
}

type Sessions interface {
	Open(ctx context.Context) (*Session, error)
	DeleteSession(*Session)
}
type FSTaps interface {
	Stat(ctx context.Context, dentry casfuse.Dentry, node TapNode)
	Mkdir(ctx context.Context, dentry casfuse.Dentry, dir TapDir)
	Symlink(ctx context.Context, dentry casfuse.Dentry, node TapNode)
	Unlink(ctx context.Context, dentry casfuse.Dentry, node TapNode)
	Rename(ctx context.Context, oldDir TapDir, oldDentry casfuse.Dentry, newDir TapDir, newDentry casfuse.Dentry, node, replacedNode TapNode)
	OpenFile(ctx context.Context, dentry casfuse.Dentry, node TapNode, readOnly bool)
	AttrChangedFile(ctx context.Context, dentry casfuse.Dentry, node TapNode)
	ReadLink(ctx context.Context, dentry casfuse.Dentry, n TapNode)
	Link(ctx context.Context, node TapNode, source, dentry casfuse.Dentry)
}

type Tap interface {
	Sessions
	FSTaps
}

type tap struct {
	sessionMap sync.Map
	hasher     hash.Hasher
}

func sessionIndex(pid uint32) (int, error) {
	return syscall.Getpgid(int(pid))
}

func (t *tap) Open(ctx context.Context) (*Session, error) {
	id, ok := t.Id(ctx)
	if !ok {
		return nil, casfuse.ErrInvalidSession
	}
	t.DeleteSessionId(id)
	// Create a new session
	session := newSession(id, t.hasher)

	if _, ok := t.sessionMap.LoadOrStore(id, session); ok {
		return nil, casfuse.ErrSessionAlreadyExists
	}
	return session, nil

}
func (t *tap) DeleteSession(s *Session) {
	t.DeleteSessionId(s.sid)
}
func (t *tap) DeleteSessionId(id int) {
	t.sessionMap.LoadAndDelete(id)
}

func (t *tap) Id(ctx context.Context) (int, bool) {
	ctxData := ctx.Value(casfuse.ContextKey)
	if ctxData == nil {
		return 0, false
	}
	pid := ctxData.(*casfuse.ContextValue).Pid
	pgid, err := sessionIndex(pid)
	if err != nil {
		return 0, false
	}
	return pgid, true
}
func (t *tap) sessionById(id int) *Session {
	val, ok := t.sessionMap.Load(id)
	if !ok {
		return nil
	}
	return val.(*Session)
}
func (t *tap) Session(ctx context.Context) *Session {
	id, ok := t.Id(ctx)
	if !ok {
		return nil
	}
	return t.sessionById(id)
}
func (t *tap) Stat(ctx context.Context, dentry casfuse.Dentry, node TapNode) {
	session := t.Session(ctx)
	if session == nil {
		return
	}
	session.Stat(node, dentry)
}
func (t *tap) Mkdir(ctx context.Context, dentry casfuse.Dentry, dir TapDir) {
	session := t.Session(ctx)
	if session == nil {
		return
	}
	session.Mkdir(dir, dentry)
}
func (t *tap) Symlink(ctx context.Context, dentry casfuse.Dentry, node TapNode) {
	session := t.Session(ctx)
	if session == nil {
		return
	}
	session.Symlink(node, dentry)
}
func (t *tap) Unlink(ctx context.Context, dentry casfuse.Dentry, node TapNode) {
	session := t.Session(ctx)
	if session == nil {
		return
	}
	session.Unlink(node, dentry)
}
func (t *tap) Rename(ctx context.Context, oldDir TapDir, oldDentry casfuse.Dentry, newDir TapDir, newDentry casfuse.Dentry, node, replaceNode TapNode) {
	session := t.Session(ctx)
	if session == nil {
		return
	}
	session.Rename(oldDir, oldDentry, newDir, newDentry, node, replaceNode)
}
func (t *tap) OpenFile(ctx context.Context, dentry casfuse.Dentry, node TapNode, readOnly bool) {
	session := t.Session(ctx)
	if session == nil {
		return
	}
	session.Open(node, dentry, readOnly)
}
func (t *tap) AttrChangedFile(ctx context.Context, dentry casfuse.Dentry, node TapNode) {
	session := t.Session(ctx)
	if session == nil {
		return
	}
	session.AttrChanged(node, dentry)
}
func (t *tap) ReadLink(ctx context.Context, dentry casfuse.Dentry, n TapNode) {
	session := t.Session(ctx)
	if session == nil {
		return
	}
	session.ReadLink(n, dentry)
}
func (t *tap) Link(ctx context.Context, node TapNode, source, dentry casfuse.Dentry) {
	session := t.Session(ctx)
	if session == nil {
		return
	}
	session.Link(ctx, node, source, dentry)
}
