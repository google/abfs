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
	"time"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse"
)

type FileAudit interface {
	OpenAudit(ctx context.Context, dentry casfuse.Dentry, readOnly bool)
	SetAttrAudit(context.Context, casfuse.Dentry)
	AttrAudit(context.Context, casfuse.Dentry)
}
type File struct {
	baseNode
	blob   Blob
	openNr int
	audit  FileAudit
}

func (f *File) SetAudit(a FileAudit) {
	f.audit = a
}

// Call in excess here to keep file stat information up to date
func (f *File) updateBlobInfo() {
	f.SetSize(f.blob.Size())
}
func (f *File) openCount() int {
	return f.openNr
}
func (f *File) incOpenCount() {
	f.openNr++
}

func (f *File) decOpenCount() {
	f.openNr--
	if f.openNr == 0 {
		f.blob.Close()
	}
}
func (f *File) doOpen() error {
	var err error
	if f.openCount() == 0 {
		err = f.blob.Open()
	}

	if err == nil {
		f.incOpenCount()
	}

	return err
}

func (f *File) Open(ctx context.Context, dentry casfuse.Dentry, truncate, readOnly bool) error {
	f.lock.Lock()
	defer f.lock.Unlock()
	defer f.updateBlobInfo()
	ts := time.Now()

	err := f.doOpen()
	if err != nil {
		return err
	}
	if truncate {
		if err := f.blob.Truncate(); err != nil {
			return err
		}
		f.SetMtime(ts)
	}
	f.audit.OpenAudit(ctx, dentry, readOnly)
	return nil
}
func (f *File) Close() error {
	f.lock.Lock()
	f.decOpenCount()
	f.lock.Unlock()
	return nil

}
func (f *File) Release(ctx context.Context) error {
	f.lock.Lock()
	f.decOpenCount()
	f.lock.Unlock()
	return nil
}

func (f *File) Fallocate(ctx context.Context, mode int, offset, len int64) error {
	ts := time.Now()
	f.lock.Lock()
	defer f.lock.Unlock()
	defer f.updateBlobInfo()

	err := f.blob.Fallocate(mode, offset, len)
	if err != nil {
		f.SetMtime(ts)
		f.SetCtime(ts)
	}
	return err

}

func (f *File) Flush(ctx context.Context) error {
	f.lock.Lock()
	defer f.lock.Unlock()
	defer f.updateBlobInfo()
	return f.blob.Flush()
}

func (f *File) ReadAt(p []byte, off int64) (int, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	return f.blob.ReadAt(p, off)
}
func (f *File) WriteAt(p []byte, off int64) (int, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	defer f.updateBlobInfo()
	return f.blob.WriteAt(p, off)
}
func (f *File) Fsync(ctx context.Context) error {
	f.lock.Lock()
	defer f.lock.Unlock()
	defer f.updateBlobInfo()
	return f.blob.Fsync()
}
func (f *File) Lseek(ctx context.Context, offset int64, whence uint32) (int64, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	defer f.updateBlobInfo()
	return f.blob.Lseek(offset, whence)
}
func (f *File) Poll(ctx context.Context) (bool, bool) {
	f.lock.Lock()
	defer f.lock.Unlock()
	return f.blob.Poll()

}
func (f *File) SetAttr(ctx context.Context, dentry casfuse.Dentry, size *int64, atime, mtime *time.Time, mode *cas.FileMode) {
	f.lock.Lock()
	defer f.lock.Unlock()
	defer f.updateBlobInfo()

	change := false

	if size != nil {
		change = true
		f.blob.SetSize(*size)
	}
	if atime != nil {
		change = true
		f.SetAtime(*atime)
	}
	if mtime != nil {
		change = true
		f.SetMtime(*mtime)
	}
	if mode != nil {
		if *mode != f.Mode() {
			change = true
			f.inode.mode = *mode
		}
	}
	if change {
		f.audit.SetAttrAudit(ctx, dentry)
	}
}

func (f *File) Blob() Blob {
	return f.blob
}

// re open a blob if we have an open count
func (f *File) Reopen() error {
	var err error
	if f.openCount() != 0 {
		err = f.blob.Open()
	}
	return err
}
func (f *File) Attr(ctx context.Context, dentry casfuse.Dentry) *casfuse.NodeAttr {
	f.audit.AttrAudit(ctx, dentry)
	return f.getAttr()
}
