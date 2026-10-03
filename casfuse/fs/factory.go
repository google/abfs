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
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse"
)

var _ casfuse.Dir = (*Dir)(nil)
var _ casfuse.File = (*File)(nil)
var _ casfuse.SymLink = (*SymLink)(nil)
var _ casfuse.Fifo = (*Fifo)(nil)
var _ casfuse.Socket = (*Socket)(nil)
var _ DirAudit = (*audit)(nil)
var _ FileAudit = (*audit)(nil)

func NewRootDir(f *FS) *Dir {
	return &Dir{
		baseNode: *f.newBaseNode(nil, cas.FileModeDir()),
		entries:  make(map[string]Node),
		audit:    audit{},
	}
}
func NewDir(parent FsDir, m cas.FileMode, nf NodeFactory) *Dir {
	return &Dir{
		baseNode: *parent.fs().newBaseNode(parent, m),
		entries:  make(map[string]Node),
		nf:       nf,
		audit:    audit{},
	}
}

func NewFile(parent FsDir, m cas.FileMode, blob Blob) *File {
	file := &File{baseNode: *parent.fs().newBaseNode(parent, m),
		blob:  blob,
		audit: audit{}}
	if blob != nil {
		file.updateBlobInfo()
	}
	return file
}
func NewSymLink(parent FsDir, m cas.FileMode) *SymLink {
	return &SymLink{
		baseNode: *parent.fs().newBaseNode(parent, m),
		audit:    audit{},
	}
}
func NewFifo(parent FsDir, m cas.FileMode) *Fifo {
	return &Fifo{baseNode: *parent.fs().newBaseNode(parent, m)}
}
func NewSocket(parent FsDir, m cas.FileMode) *Socket {
	return &Socket{baseNode: *parent.fs().newBaseNode(parent, m)}
}
