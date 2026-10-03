// Copyright 2026 Google LLC
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

package bindcas

import (
	"context"
	"path"

	"github.com/google/abfs/shared/entities"
)

const GitDirName = ".git"

type Node interface {
	Path() string
	SetExcluded(context.Context, bool) error
}
type Dir interface {
	Node
	Symlink(ctx context.Context, name, target string) (Node, error)
	MkdirAll(ctx context.Context, name string) (Dir, error)
	DeepUpdate(ctx context.Context, deepTreeHash entities.Hash) error
	CreateControl(ctx context.Context) error
	CreateGit(ctx context.Context, hash entities.Hash) error
	CreateGitObjects(ctx context.Context, name string) error
	Mknod(ctx context.Context, name string, mode entities.FileMode) (Node, error)
}

// Casfs dir object
type casfsNode struct {
	name   string
	parent *casfsDir
	casfs  CASFS
}

func (n *casfsNode) Path() string {
	p := ""
	if n.parent != nil {
		p = n.parent.Path()
	}
	return path.Join(p, n.name)
}
func (n *casfsNode) SetExcluded(ctx context.Context, val bool) error {
	return n.casfs.SetExcluded(path.Join(n.Path()), val)
}

type casfsDir struct {
	casfsNode
}

func CreateRootDir(casfs CASFS, path string) *casfsDir {
	return &casfsDir{casfsNode{name: ".", parent: nil, casfs: casfs}}
}
func CreateDir(casfs CASFS, parent *casfsDir, name string) *casfsDir {
	return &casfsDir{casfsNode{name: name, parent: parent, casfs: casfs}}
}

func (d *casfsDir) MkdirAll(ctx context.Context, name string) (Dir, error) {
	fullPath := path.Join(d.Path(), name)
	if err := d.casfs.MkdirAll(fullPath, 0750); err != nil {
		return nil, err
	}
	return CreateDir(d.casfs, d, name), nil
}
func (d *casfsDir) DeepUpdate(ctx context.Context, deepTreeHash entities.Hash) error {
	return d.casfs.SetDeepTree(d.Path(), deepTreeHash)
}
func (d *casfsDir) Symlink(ctx context.Context, name, target string) (Node, error) {
	err := d.casfs.Symlink(target, path.Join(d.Path(), name))
	if err != nil {
		return nil, err
	}
	return &casfsNode{name: name, parent: d, casfs: d.casfs}, nil
}
func (d *casfsDir) CreateControl(ctx context.Context) error {
	return d.casfs.CreateControl(d.Path())
}
func (d *casfsDir) CreateGit(ctx context.Context, hash entities.Hash) error {
	return d.casfs.CreateGit(d.Path(), GitDirName, hash.String())
}
func (d *casfsDir) CreateGitObjects(ctx context.Context, name string) error {
	return d.casfs.CreateGitObjects(d.Path(), name)
}
func (d *casfsDir) Mknod(ctx context.Context, name string, mode entities.FileMode) (Node, error) {
	if err := d.casfs.Mknod(path.Join(d.Path(), name), mode, 0); err != nil {
		return nil, err
	}
	return &casfsNode{name: name, parent: d, casfs: d.casfs}, nil
}
