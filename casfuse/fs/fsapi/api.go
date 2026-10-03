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

package fsapi

import (
	"context"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs/fsapi/ctrl"
)

type FS interface {
	Ready() bool
	Hasher() hash.Hasher
}
type Node interface {
	IsDebug() bool
	SetDebug(val bool)
	IsDirty() bool
	IsLoaded() bool
	IsExcluded() bool

	SetExcluded(bool)
	Snapshot(ctx context.Context) (hash.Hash, int64, error)
	ForceHashAndSize(hash.Hash, int64) error
}
type DirNode interface {
	Node

	ForceLoadAll(ctx context.Context) error
	SnapshotDirList(ctx context.Context) (hash.Hash, error)
	SetDeepTree(hash.Hash) error
	AddChild(name string, inode casfuse.Inode)
}

type CASApi struct {
	fs FS
}

func NewCASAPI(fs FS) casfuse.API {
	return &CASApi{fs: fs}
}

func (c *CASApi) Status() (*fs.Status, error) {
	return &fs.Status{Ready: c.fs.Ready()}, nil

}
func (*CASApi) Type(inode casfuse.Inode) cas.FileMode {
	return inode.Mode()
}
func (*CASApi) Dirty(inode casfuse.Inode) (bool, error) {
	if n, ok := inode.(Node); ok {
		return n.IsDirty(), nil
	}
	return false, casfuse.ErrNotCASNode
}

func (*CASApi) DirList(ctx context.Context, inode casfuse.Inode) (hash.Hash, error) {
	if d, ok := inode.(DirNode); ok {
		return d.SnapshotDirList(ctx)
	}
	return nil, casfuse.ErrNotCASNode
}

func (*CASApi) Loaded(inode casfuse.Inode) (bool, error) {
	if n, ok := inode.(Node); ok {
		return n.IsLoaded(), nil
	}
	return false, casfuse.ErrNotCASNode
}

func (*CASApi) Excluded(inode casfuse.Inode) (bool, error) {
	if n, ok := inode.(Node); ok {
		return n.IsExcluded(), nil
	}
	return false, casfuse.ErrNotCASNode
}

func (*CASApi) SetExcluded(inode casfuse.Inode, val bool) error {
	if n, ok := inode.(Node); ok {
		n.SetExcluded(val)
		return nil
	}
	return casfuse.ErrNotCASNode
}
func (*CASApi) Hash(ctx context.Context, inode casfuse.Inode) (hash.Hash, error) {
	if n, ok := inode.(Node); ok {
		h, _, err := n.Snapshot(ctx)
		return h, err
	}
	return nil, casfuse.ErrNotCASNode
}
func (*CASApi) SetHash(inode casfuse.Inode, hash hash.Hash) error {
	if n, ok := inode.(Node); ok {
		return n.ForceHashAndSize(hash, 0)

	}
	return casfuse.ErrNotCASNode

}
func (*CASApi) HashStat(ctx context.Context, inode casfuse.Inode) (*fs.HashStat, error) {
	if n, ok := inode.(Node); ok {
		h, sz, err := n.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		return &fs.HashStat{Hash: h, Mode: inode.Mode(), Size: sz}, nil
	}
	return nil, casfuse.ErrNotCASNode
}

func (*CASApi) HashSize(ctx context.Context, inode casfuse.Inode) (hash.Hash, int64, error) {
	if n, ok := inode.(Node); ok {
		return n.Snapshot(ctx)
	}
	return nil, 0, casfuse.ErrNotCASNode
}
func (*CASApi) SetHashSize(inode casfuse.Inode, hash hash.Hash, size int64) error {
	if n, ok := inode.(Node); ok {
		return n.ForceHashAndSize(hash, size)
	}
	return casfuse.ErrNotCASNode

}

func (*CASApi) SetDeepTree(inode casfuse.Inode, hash hash.Hash) error {
	if d, ok := inode.(DirNode); ok {
		return d.SetDeepTree(hash)
	}
	return casfuse.ErrNotCASNode
}

func (*CASApi) Debug(inode casfuse.Inode) (bool, error) {
	if n, ok := inode.(Node); ok {
		return n.IsDebug(), nil
	}
	return false, casfuse.ErrNotCASNode
}
func (*CASApi) SetDebug(inode casfuse.Inode, val bool) error {
	if n, ok := inode.(Node); ok {
		n.SetDebug(val)
		return nil
	}
	return casfuse.ErrNotCASNode

}
func (*CASApi) CreateControl(ctx context.Context, inode casfuse.Inode, directory string) error {
	if d, ok := inode.(DirNode); ok {
		n, err := ctrl.CreateCtrl(ctx, inode)
		if err != nil {
			return err
		}
		d.AddChild(directory, n)
		return nil
	}
	return casfuse.ErrNotCASNode
}
func (*CASApi) CreateRamfs(ctx context.Context, inode casfuse.Inode, directory string) error {
	if d, ok := inode.(DirNode); ok {
		n, err := ctrl.CreateRamfs(ctx, inode)
		if err != nil {
			return err
		}
		d.AddChild(directory, n)
		return nil
	}
	return casfuse.ErrNotCASNode
}
func (c *CASApi) CreateGit(ctx context.Context, inode casfuse.Inode, directory, head string) error {
	hash, err := c.fs.Hasher().NewHash(head)
	if err != nil {
		return nil
	}
	if d, ok := inode.(DirNode); ok {
		n, err := ctrl.CreateGit(ctx, inode, hash)
		if err != nil {
			return err
		}
		d.AddChild(directory, n)
		return nil
	}
	return casfuse.ErrNotCASNode
}
func (*CASApi) CreateGitObjects(ctx context.Context, inode casfuse.Inode, directory string) error {
	if d, ok := inode.(DirNode); ok {
		n, err := ctrl.CreateGitObjects(ctx, inode)
		if err != nil {
			return err
		}
		d.AddChild(directory, n)
		return nil
	}
	return casfuse.ErrNotCASNode
}
