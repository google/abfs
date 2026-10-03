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

package ctrl

import (
	"context"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"

	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
	"github.com/google/abfs/casfuse/fs/fsapi/ctrl/git"
	ctrltap "github.com/google/abfs/casfuse/fs/fsapi/ctrl/tap"
	"github.com/google/abfs/casfuse/fs/fscas/tap"
	"github.com/google/abfs/casfuse/fs/mem"
)

type NoNodes struct {
}

func (NoNodes) CreateDirNode(mode cas.FileMode) (fs.Node, error) {
	return nil, casfuse.ErrNotSupported
}
func (NoNodes) CreateFileNode(mode cas.FileMode) (fs.Node, error) {
	return nil, casfuse.ErrNotSupported
}
func (NoNodes) CreateSymLinkNode(mode cas.FileMode) (fs.Node, error) {
	return nil, casfuse.ErrNotSupported
}
func (NoNodes) CreateFifoNode(mode cas.FileMode) (fs.Node, error) {
	return nil, casfuse.ErrNotSupported
}
func (NoNodes) CreateSocketNode(mode cas.FileMode) (fs.Node, error) {
	return nil, casfuse.ErrNotSupported
}

func CreateCtrl(ctx context.Context, parent casfuse.Inode) (casfuse.Inode, error) {
	var newDir fs.FsDir
	var parentDir fs.FsDir = parent.(fs.FsDir)
	newDir = fs.NewDir(parentDir, cas.FileModeDir(), NoNodes{})

	tap := parent.FS().GetExt(casfuse.CASTap).(tap.Sessions)
	tapDir := ctrltap.NewDir(newDir, tap)
	newDir.AddChild("tap", tapDir)
	return newDir, nil
}
func CreateRamfs(ctx context.Context, parent casfuse.Inode) (fs.Node, error) {
	var parentDir fs.FsDir = parent.(fs.FsDir)
	return fs.NewDir(parentDir, cas.FileModeDir(), mem.NewNodeFactory(parentDir)), nil

}
func CreateGit(ctx context.Context, parent casfuse.Inode, head hash.Hash) (fs.Node, error) {
	// For now assume project root is above git directory
	var parentDir fs.FsDir = parent.(fs.FsDir)
	if err := parentDir.ForceLoadAll(ctx); err != nil {
		return nil, err
	}

	// Create git project directory
	newDir := fs.NewDir(parentDir, cas.FileModeDir(), mem.NewNodeFactory(parentDir))
	if err := git.ProjectCreate(newDir, parentDir, head); err != nil {
		return nil, err
	}

	return newDir, nil
}
func CreateGitObjects(ctx context.Context, parent casfuse.Inode) (fs.Node, error) {
	var parentDir fs.FsDir = parent.(fs.FsDir)

	return git.NewObjectsDir(parentDir), nil
}
