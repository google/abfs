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

	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
)

type file struct {
	fs.File
	node
}

func (s *file) ForceLoadAll(ctx context.Context) error {
	return nil
}

func (f *file) updateInodeFromBlob(size int64) {
	f.SetSize(size)
}
func (f *file) SetExcluded(val bool) {
	changed := false

	f.Lock()
	defer func() {
		if changed {
			f.isExcluded = val
		}
		f.Unlock()
		if changed {
			f.Parent().(*dir).childChanged()
		}
	}()
	changed = val != f.isExcluded
}
func (f *file) ForceHashAndSize(hash hash.Hash, size int64) error {
	changed := false
	f.Lock()
	defer func() {
		excluded := f.isExcluded
		f.Unlock()
		if !excluded && changed {
			f.Parent().(*dir).childChanged()
		}
	}()
	// Fix so blob uses file's dirty attribute direcly
	if f.Blob().(*blob).isDirty() {
		return casfuse.ErrDirty
	}
	if hash.Equals(f.hash) {
		return nil
	}

	if size == 0 && hash.IsEmptyFile() {
		// Need to resolve size

	}
	f.Blob().Close()
	f.hash = hash
	f.SetSize(size)
	f.isLoaded = false
	changed = true
	return nil
}
func (f *file) Snapshot(context.Context) (hash.Hash, int64, error) {
	f.Lock()
	defer f.Unlock()

	// check lock for all calls
	hash, err := f.Blob().(*blob).Snapshot()
	if err == casfuse.ErrNotDirty {
		return f.hash, f.Size(), nil
	}
	// Update our hash
	f.hash = hash
	err = f.Reopen()

	return f.hash, f.Size(), err

}
func (f *file) markDirty() {
	f.MakeDirty()
	f.Parent().(*dir).childChanged()
}
func (f *file) OpenAudit(ctx context.Context, dentry casfuse.Dentry, readOnly bool) {
	GetTap(f.FS()).OpenFile(ctx, dentry, f, readOnly)
}
func (f *file) SetAttrAudit(ctx context.Context, dentry casfuse.Dentry) {
	GetTap(f.FS()).AttrChangedFile(ctx, dentry, f)
}
func (f *file) AttrAudit(ctx context.Context, dentry casfuse.Dentry) {
	GetTap(f.FS()).Stat(ctx, dentry, f)
}
