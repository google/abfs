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

	"github.com/google/abfs/casfuse"
)

// no-op audit object
type audit struct {
}

func (audit) MkdirAudit(context.Context, casfuse.Dentry, casfuse.Inode) {
}
func (audit) CreateFileAudit(context.Context, casfuse.Dentry, casfuse.Inode) {
}
func (audit) SymlinkAudit(context.Context, casfuse.Dentry, casfuse.Inode) {
}
func (audit) LinkAudit(context.Context, casfuse.Dentry, casfuse.Dentry) {
}
func (audit) UnlinkAudit(context.Context, casfuse.Dentry, casfuse.Inode) {
}
func (audit) RenameAudit(context.Context, *RenameAuditInfo) {
}
func (audit) OpenAudit(context.Context, casfuse.Dentry, bool) {
}
func (audit) SetAttrAudit(context.Context, casfuse.Dentry) {
}
func (audit) ReleaseAudit(context.Context) {
}
func (audit) AttrAudit(context.Context, casfuse.Dentry) {
}
func (audit) ReadLinkAudit(context.Context, casfuse.Dentry) {
}
