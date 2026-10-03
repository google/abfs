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
	"context"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
	"github.com/google/abfs/casfuse/fs/fscas/tap"
)

type dir struct {
	fs.Dir
	sessions tap.Sessions
}

func (d *dir) CreateTmpFile(ctx context.Context, dentry casfuse.Dentry, mode cas.FileMode) (casfuse.Inode, error) {
	session, err := d.sessions.Open(ctx)
	if err != nil {
		return nil, err
	}
	return NewFile(d, session, d.sessions), nil
}
