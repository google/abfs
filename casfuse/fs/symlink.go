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

type SymLinkAudit interface {
	ReadLinkAudit(context.Context, casfuse.Dentry)
	AttrAudit(context.Context, casfuse.Dentry)
}
type SymLink struct {
	baseNode
	target string
	audit  SymLinkAudit
}

func (s *SymLink) SetAudit(a SymLinkAudit) {
	s.audit = a
}
func (s *SymLink) SetTarget(ctx context.Context, target string) {
	s.target = target
	s.SetSize(int64(len(target)))
}
func (s *SymLink) Target(ctx context.Context, dentry casfuse.Dentry) (string, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.audit.ReadLinkAudit(ctx, dentry)
	return s.target, nil
}
func (s *SymLink) TargetLocked() string {
	return s.target
}
func (s *SymLink) Attr(ctx context.Context, dentry casfuse.Dentry) *casfuse.NodeAttr {
	s.audit.AttrAudit(ctx, dentry)
	return s.getAttr()
}
