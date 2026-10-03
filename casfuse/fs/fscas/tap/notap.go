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

	"github.com/google/abfs/casfuse"
)

type NoTap struct {
}

func (NoTap) Open(ctx context.Context) (*Session, error) {
	return nil, casfuse.ErrNotSupported
}

func (NoTap) DeleteSession(*Session) {
}

func (NoTap) Stat(ctx context.Context, dentry casfuse.Dentry, node TapNode) {
}
func (NoTap) Mkdir(ctx context.Context, dentry casfuse.Dentry, dir TapDir) {
}
func (NoTap) Symlink(ctx context.Context, dentry casfuse.Dentry, node TapNode) {
}

func (NoTap) Unlink(ctx context.Context, dentry casfuse.Dentry, node TapNode) {
}
func (NoTap) Rename(ctx context.Context, oldDir TapDir, oldDentry casfuse.Dentry, newDir TapDir, newDentry casfuse.Dentry, node, replacedNode TapNode) {
}
func (NoTap) OpenFile(ctx context.Context, dentry casfuse.Dentry, node TapNode, readOnly bool) {
}
func (NoTap) AttrChangedFile(ctx context.Context, dentry casfuse.Dentry, node TapNode) {
}
func (NoTap) ReadLink(ctx context.Context, dentry casfuse.Dentry, n TapNode) {
}
func (NoTap) Link(ctx context.Context, node TapNode, source, dentry casfuse.Dentry) {
}
