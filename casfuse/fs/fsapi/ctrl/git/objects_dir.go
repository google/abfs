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

package git

import (
	"context"
	"unicode"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
	"github.com/google/abfs/casfuse/fs/mem"
)

type objectsDir struct {
	fs.Dir
}

func NewObjectsDir(parent fs.FsDir) *objectsDir {
	out := &objectsDir{
		Dir: *fs.NewDir(parent, cas.FileModeDir(), nil),
	}
	return out
}
func isxdigit(r rune) bool {
	return unicode.Is(unicode.Hex_Digit, r)
}
func isValidHex(name string) bool {
	for _, i := range name {
		if !isxdigit(i) {
			return false
		}
	}
	return true
}
func (d *objectsDir) isObjDirName(name string) bool {
	if len(name) == 2 || len(name) == 3 {
		return isValidHex(name)
	}
	return false
}
func (d *objectsDir) Lookup(ctx context.Context, dentry casfuse.Dentry) (casfuse.Inode, error) {
	// Need to do lookup / create all within the lock
	d.Lock()
	defer d.Unlock()

	entries := d.InternalEntriesLocked()
	node, ok := (*entries)[dentry.Name()]
	if ok {
		return node, nil
	}
	if dentry.Name() == "pack" {
		newDir := fs.NewDir(d, cas.FileModeDir(), mem.NewNodeFactory(d))
		d.AddChild(dentry.Name(), newDir)
		return newDir, nil

	}
	if d.isObjDirName(dentry.Name()) {
		// Create object directory
		newDir := newObjectDir(d)
		d.AddChild(dentry.Name(), newDir)
		return newDir, nil
	}

	return nil, casfuse.ErrEntryNotFound
}
