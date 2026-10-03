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

package ctrltap

import (
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
	"github.com/google/abfs/casfuse/fs/fscas/tap"
)

var _ fs.Blob = (*blob)(nil)
var _ casfuse.Dir = (*dir)(nil)

func NewDir(parent fs.FsDir, sessions tap.Sessions) *dir {
	return &dir{
		Dir:      *fs.NewDir(parent, cas.FileModeDir(), nil),
		sessions: sessions,
	}
}

func NewFile(parent fs.FsDir, session *tap.Session, sessions tap.Sessions) casfuse.Inode {
	blob := &blob{session: session, sessions: sessions}
	return fs.NewFile(parent, cas.FileModeFile(), blob)
}
