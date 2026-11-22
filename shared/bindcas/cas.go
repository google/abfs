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
	"errors"
	"fmt"
	"path"
	"syscall"

	"github.com/google/abfs/shared/entities"
)

const (
	GitDirName = ".git"
	// NodeTypeCommit is the casfs type of a submodule entry (gitlink).
	NodeTypeCommit = "commit"
)

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
	return SetWorkTreeDeepTree(d.casfs, d.Path(), deepTreeHash)
}

// SetWorkTreeDeepTree sets the deeptree of the work tree dir. casfs refuses a deeptree on a
// submodule entry of the tree around dir; that entry is replaced first (see
// ReplaceSubmoduleEntry).
func SetWorkTreeDeepTree(casfs CASFS, dir string, deepTreeHash entities.Hash) error {
	err := casfs.SetDeepTree(dir, deepTreeHash)
	if !errors.Is(err, syscall.EOPNOTSUPP) {
		return err
	}
	replaced, replaceErr := ReplaceSubmoduleEntry(casfs, dir)
	if replaceErr != nil {
		return fmt.Errorf("failed to replace %s, which takes no deeptree: %w", dir, replaceErr)
	}
	if !replaced {
		return err
	}
	return casfs.SetDeepTree(dir, deepTreeHash)
}

// ReplaceSubmoduleEntry replaces dir with a plain directory that keeps its exclusion when casfs
// shows it as a submodule entry, an empty directory of type commit that takes no deeptree. git
// checks a repository out into a submodule entry the same way. Any other node is left alone. It
// reports whether dir was replaced.
func ReplaceSubmoduleEntry(casfs CASFS, dir string) (bool, error) {
	nodeType, err := casfs.NodeType(dir)
	if err != nil {
		return false, fmt.Errorf("failed to read the node type of %s: %w", dir, err)
	}
	if nodeType != NodeTypeCommit {
		return false, nil
	}
	excluded, err := casfs.Excluded(dir)
	if err != nil {
		return false, fmt.Errorf("failed to read the exclusion of %s: %w", dir, err)
	}
	if err := casfs.Remove(dir); err != nil {
		return false, fmt.Errorf("failed to remove the submodule entry %s: %w", dir, err)
	}
	if err := casfs.Mkdir(dir, 0755); err != nil {
		return false, fmt.Errorf("failed to create directory %s, the submodule entry there is removed: %w", dir, err)
	}
	if excluded {
		if err := casfs.SetExcluded(dir, true); err != nil {
			return false, fmt.Errorf("failed to exclude %s, the directory is created without its exclusion: %w", dir, err)
		}
	}
	return true, nil
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
