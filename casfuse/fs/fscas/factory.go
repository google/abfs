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
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
)

var _ casfuse.Dir = (*dir)(nil)
var _ casfuse.SymLink = (*symLink)(nil)

var _ Node = (*file)(nil)
var _ Node = (*symLink)(nil)
var _ Node = (*dir)(nil)

func NewRootDir(f *fs.FS) {
	out := &dir{
		Dir:  *fs.NewRootDir(f),
		node: *NewNode(f.Hasher().EmptyDirHash()),
	}
	out.SetNodeFactory(out)
	out.SetAudit(out)
	f.SetRoot(out)
}
func CreateDir(parent fs.FsDir, t cas.FileMode) Node {
	out := &dir{
		Dir:  *fs.NewDir(parent, t, nil),
		node: *NewNode(parent.FS().Hasher().EmptyDirHash()),
	}
	out.SetNodeFactory(out)
	out.SetAudit(out)
	return out
}
func CreateFile(parent fs.FsDir, t cas.FileMode) Node {
	blob := &blob{}
	out := &file{
		File: *fs.NewFile(parent, t, blob),
		node: *NewNode(parent.FS().Hasher().EmptyFileHash()),
	}
	blob.file = out
	blob.Init()
	out.SetAudit(out)
	return out
}
func CreateSymLink(parent fs.FsDir, t cas.FileMode) Node {
	out := &symLink{
		SymLink: *fs.NewSymLink(parent, t),
		node:    *NewNode(parent.FS().Hasher().ZeroHash()),
	}
	out.SetAudit(out)
	return out
}
