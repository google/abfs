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

type symLink struct {
	fs.SymLink
	node
}

func (s *symLink) ForceLoadAll(ctx context.Context) error {
	return nil
}
func (s *symLink) load(ctx context.Context) error {
	cas := GetStore(s.FS())
	target, err := cas.GetSymLink(s.hash)
	if err != nil {
		return err
	}
	s.SymLink.SetTarget(ctx, target)
	s.SetLoaded()
	return nil
}
func (s *symLink) ensureLoaded(ctx context.Context) error {
	s.Lock()
	defer s.Unlock()
	if s.IsLoaded() {
		return nil
	}
	return s.load(ctx)
}
func (s *symLink) SetTarget(ctx context.Context, target string) {
	s.SymLink.SetTarget(ctx, target)

	s.Lock()
	s.SetLoaded()
	s.MakeDirty()
	s.Unlock()
	// Match kmod behavoir.  TODO/change to to not auto snapshot
	s.Snapshot(ctx)

	s.Parent().(*dir).childChanged()

}
func (s *symLink) Target(ctx context.Context, dentry casfuse.Dentry) (string, error) {
	if err := s.ensureLoaded(ctx); err != nil {
		return "", err
	}
	return s.SymLink.Target(ctx, dentry)
}
func (s *symLink) SetExcluded(val bool) {
	changed := false

	s.Lock()
	defer func() {
		if changed {
			s.isExcluded = val
		}
		s.Unlock()
		if changed {
			s.Parent().(*dir).childChanged()
		}
	}()
	changed = val != s.isExcluded
}

func (s *symLink) ForceHashAndSize(hash hash.Hash, size int64) error {
	s.Lock()
	defer s.Unlock()

	if s.IsDirty() {
		return casfuse.ErrDirty
	}
	s.hash = hash
	s.SetSize(size)
	s.isLoaded = false
	// Update parent
	return nil
}

func (s *symLink) Snapshot(context.Context) (hash.Hash, int64, error) {
	s.Lock()
	defer s.Unlock()

	if !s.IsDirty() {
		return s.hash, s.Size(), nil
	}
	cas := GetStore(s.FS())
	hash, err := cas.SaveSymLink(s.TargetLocked())
	if err != nil {
		return nil, 0, err
	}
	s.hash = hash
	s.ClearDirty()
	return s.hash, s.Size(), nil

}

func (s *symLink) AttrAudit(ctx context.Context, dentry casfuse.Dentry) {
	GetTap(s.FS()).Stat(ctx, dentry, s)
}
func (s *symLink) ReadLinkAudit(ctx context.Context, dentry casfuse.Dentry) {
	GetTap(s.FS()).ReadLink(ctx, dentry, s)
}
