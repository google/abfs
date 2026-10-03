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

package casfuse

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/fs/gitindex"
	"github.com/google/abfs/cas/hash"
)

// Want to return os errors from these functions
var (
	ErrInvalid  = os.ErrInvalid
	ErrNotExist = os.ErrNotExist
)

func pathError(op, path string, err error) *os.PathError {
	return &os.PathError{
		Op:   op,
		Path: path,
		Err:  err,
	}
}
func linkError(op, oldPath, newPath string, err error) *os.LinkError {
	return &os.LinkError{
		Op:  op,
		Old: oldPath,
		New: newPath,
		Err: err,
	}
}

type attr struct {
	n    *NodeAttr
	name string
}

func (a attr) Name() string {
	return a.name
}
func (a attr) Size() int64 {
	return a.n.Size
}
func (a attr) Mode() cas.FileMode {
	return a.n.Mode
}

func (a attr) ModTime() time.Time {
	return a.n.Mtime
}

func (a attr) IsDir() bool {
	return a.n.Mode.IsDir()
}
func (a attr) Sys() any {
	return nil
}

type namei struct {
	fs FS
}

var _ fs.FSAPI = (*namei)(nil)

func NewFSAPI(fs FS) *namei {
	return (&namei{fs: fs})
}

func (n *namei) parseInputDir(path string) (ctx context.Context, parent Dentry, name string, err error) {
	// Lookup parent
	ctx = context.Background()
	dirName := filepath.Dir(path)
	name = filepath.Base(path)
	// Add support for chdir
	cwd := n.fs.Root()
	parent, err = Resolve(ctx, cwd, dirName)
	if err != nil {
		return
	}
	return

}

// Create directory for given path relative to the root dir
func (n *namei) Mkdir(path string, mode cas.FileMode) (err error) {
	defer func() {
		if err != nil {
			err = pathError("mkdir", path, err)
		}
	}()

	ctx, parent, name, err := n.parseInputDir(path)
	if err != nil {
		return err
	}
	_, err = Mkdir(ctx, parent, name, mode)
	if err != nil {
		return err
	}
	return nil
}
func (n *namei) MkdirAll(path string, perm cas.FileMode) (err error) {
	defer func() {
		if err != nil {
			err = pathError("mkdir", path, err)
		}
	}()

	ctx, parent, name, err := n.parseInputDir(path)
	if err == nil {
		// Check if already have parent dirctory
		child := Lookup(ctx, parent, name)
		if child.Inode() == nil {
			_, err = Mkdir(ctx, parent, name, perm)
			return err
		}
		if child.Inode().Mode().IsDir() {
			return nil
		}
		return ErrNotADirectory
	}
	i := len(path) - 1
	for i >= 0 && os.IsPathSeparator(path[i]) {
		i--
	}

	for i >= 0 && !os.IsPathSeparator(path[i]) {
		i--
	}
	if parent := path[:i]; len(parent) > 0 {
		err = n.MkdirAll(parent, perm)
		if err != nil {
			return err
		}
	}
	err = n.Mkdir(path, perm)
	if err != nil {
		// check for "dir/."
		dir, err1 := n.Lstat(path)
		if err1 == nil && dir.IsDir() {
			return nil
		}
		return err
	}
	return nil
}

// Remove an entry
func (n *namei) Remove(path string) (err error) {
	defer func() {
		if err != nil {
			err = pathError("remove", path, err)
		}
	}()

	ctx, parent, name, err := n.parseInputDir(path)
	if err != nil {
		return err
	}
	return Remove(ctx, parent, name)
}
func (n *namei) Symlink(oldName, target string) (err error) {
	defer func() {
		if err != nil {
			err = linkError("symlink", oldName, target, err)
		}
	}()
	// Get new name directory
	ctx, parent, name, err := n.parseInputDir(target)
	if err != nil {
		return fmt.Errorf("parse input error on symlink: %v", err)
	}
	_, err = Symlink(ctx, parent, name, oldName)
	return err
}
func (n *namei) Link(oldName, newName string) error {
	// Resolve the full path not just the parent directory
	cwd := n.fs.Root()
	ctx := context.Background()
	target, err := Resolve(ctx, cwd, oldName)
	if err != nil {
		return err
	}
	_, newParent, newName, err := n.parseInputDir(newName)
	if err != nil {
		return err
	}

	_, err = Link(ctx, newParent, newName, target)
	return err

}
func (n *namei) Rename(oldPath, newPath string) (err error) {
	defer func() {
		if err != nil {
			err = linkError("rename", oldPath, newPath, err)
		}
	}()

	ctx, oldParent, oldName, err := n.parseInputDir(oldPath)
	if err != nil {
		return err
	}
	_, newParent, newName, err := n.parseInputDir(newPath)
	if err != nil {
		return err
	}
	return Rename(ctx, oldParent, oldName, newParent, newName)
}

func (n *namei) Mknod(path string, mode cas.FileMode, rDev int) error {
	ctx, parent, name, err := n.parseInputDir(path)
	if err != nil {
		return err
	}
	_, err = Mknod(ctx, parent, name, mode, rDev)
	return err
}
func (n *namei) Lstat(path string) (cas.FileInfo, error) {
	ctx, parent, name, err := n.parseInputDir(path)
	if err != nil {
		return nil, err
	}
	return Lstat(ctx, parent, name)
}

func (n *namei) Chmod(path string, mode cas.FileMode) error {
	ctx, parent, name, err := n.parseInputDir(path)
	if err != nil {
		return err
	}
	return Chmod(ctx, parent, name, mode)
}
func (n *namei) Readlink(path string) (out string, err error) {
	defer func() {
		if err != nil {
			err = pathError("readlink", path, err)
		}
	}()

	ctx, parent, name, err := n.parseInputDir(path)
	if err != nil {
		return out, err
	}
	return Readlink(ctx, parent, name)
}
func (n *namei) OpenFile(path string, flags int, perm cas.FileMode) (out fs.FSFile, err error) {
	defer func() {
		if err != nil {
			err = pathError("open", path, err)
		}
	}()
	ctx, parent, name, err := n.parseInputDir(path)
	if err != nil {
		return nil, err
	}
	out, err = OpenFile(ctx, parent, name, flags, perm)
	return
}

func (n *namei) Stat(path string) (cas.FileInfo, error) {
	// Resolve the full path not just the parent directory
	cwd := n.fs.Root()
	ctx := context.Background()
	dentry, err := Resolve(ctx, cwd, path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve %s err: %v", path, err)
	}
	attr := dentry.Inode().Attr(ctx, dentry)
	return &FileInfo{name: filepath.Base(path), attr: attr}, nil
}

// TODO add support for change where in the tree we are
func (n *namei) Chdir(path string) error {
	return nil
}
func (n *namei) Getwd() (string, error) {
	return "", nil
}

// Provide api via dcache
type fsapi struct {
	mountPath string
	api       API
	fs        FS
}

var _ fs.FS = (*fsapi)(nil)

func NewCASFSAPI(mountPath string, api API, fs FS) fs.FS {
	return &fsapi{mountPath: mountPath, api: api, fs: fs}
}
func (f *fsapi) MountPath() string {
	return f.mountPath
}
func (f *fsapi) context() context.Context {
	return context.Background()
}
func (f *fsapi) resolve(path string) (Inode, error) {
	dentry, err := Resolve(f.context(), f.fs.Root(), path)
	if err != nil || dentry.Inode() == nil {
		return nil, ErrEntryNotFound
	}
	return dentry.Inode(), nil
}

// Dirty check if the content associated with the path has been modified but not committed
func (f *fsapi) Dirty(path string) (bool, error) {
	inode, err := f.resolve(path)
	if err != nil {
		return false, err
	}
	return f.api.Dirty(inode)
}

// Read the directory list hash at the given path, directory list contains only the
// names of the entries within a directory.
func (f *fsapi) DirList(path string) (hash.Hash, error) {
	inode, err := f.resolve(path)
	if err != nil {
		return nil, err
	}
	return f.api.DirList(f.context(), inode)
}

// Loaded checks it contents at the directory have been fetched locally to the filesystem.
func (f *fsapi) Loaded(path string) (bool, error) {
	inode, err := f.resolve(path)
	if err != nil {
		return false, err
	}
	return f.api.Loaded(inode)
}

// Excluded checks if path is configured to be excluded from parent's hash calculation
func (f *fsapi) Excluded(path string) (bool, error) {
	inode, err := f.resolve(path)
	if err != nil {
		return false, err
	}
	return f.api.Excluded(inode)
}

// SetExcluded change the excluded attribute of the path, marking the parent as dirty.
func (f *fsapi) SetExcluded(path string, val bool) error {
	inode, err := f.resolve(path)
	if err != nil {
		return err
	}
	return f.api.SetExcluded(inode, val)
}

// Hash calculate (if dirty) and return the hash of the path.
func (f *fsapi) Hash(path string) (hash.Hash, error) {
	inode, err := f.resolve(path)
	if err != nil {
		return nil, err
	}
	return f.api.Hash(f.context(), inode)
}

// SetHash set the hash of the path, only valid if the path is not yet loaded.
func (f *fsapi) SetHash(path string, h hash.Hash) error {
	inode, err := f.resolve(path)
	if err != nil {
		return err
	}
	return f.api.SetHash(inode, h)
}

// HashStat get the hash,mode, and size of the path
func (f *fsapi) HashStat(path string) (*fs.HashStat, error) {
	inode, err := f.resolve(path)
	if err != nil {
		return nil, err
	}
	return f.api.HashStat(f.context(), inode)
}

// HashSet get the hash,size of a file at the given path.
func (f *fsapi) HashSize(path string) (hash.Hash, int64, error) {
	inode, err := f.resolve(path)
	if err != nil {
		return nil, 0, err
	}
	return f.api.HashSize(f.context(), inode)
}

// SetHashSize set the hash,size for a file at the given path, only valid if the path
// is not yet loaded.
func (f *fsapi) SetHashSize(path string, h hash.Hash, s int64) error {
	inode, err := f.resolve(path)
	if err != nil {
		return err
	}
	return f.api.SetHashSize(inode, h, s)
}

// SetDeepTree set the deep tree hash for the given directory path, only valid if
// the path is not yet loaded.  After setting the deep tree hash all children
// nodes can answer Stat() call without accessing the backend.
func (f *fsapi) SetDeepTree(path string, h hash.Hash) error {
	inode, err := f.resolve(path)
	if err != nil {
		return err
	}
	return f.api.SetDeepTree(inode, h)
}

// Debug get the debug attribute of the path.
func (f *fsapi) Debug(path string) (bool, error) {
	inode, err := f.resolve(path)
	if err != nil {
		return false, err
	}
	return f.api.Debug(inode)
}

// SetDebug set dynamic debug for the given path
func (f *fsapi) SetDebug(path string, val bool) error {
	inode, err := f.resolve(path)
	if err != nil {
		return err
	}
	return f.api.SetDebug(inode, val)
}

// Create control directory under path
func (f *fsapi) CreateControl(path, directory string) error {
	inode, err := f.resolve(path)
	if err != nil {
		return err
	}
	return f.api.CreateControl(f.context(), inode, directory)
}

// Create a non-cas ramfs type directory under the input path
func (f *fsapi) CreateRamfs(path, directory string) error {
	inode, err := f.resolve(path)
	if err != nil {
		return err
	}
	return f.api.CreateRamfs(f.context(), inode, directory)
}

// Create GIT project under given path
func (f *fsapi) CreateGit(path, directory, head string) error {
	inode, err := f.resolve(path)
	if err != nil {
		return err
	}
	return f.api.CreateGit(f.context(), inode, directory, head)
}

// Create GIT objects only under the given path
func (f *fsapi) CreateGitObjects(path, directory string) error {
	inode, err := f.resolve(path)
	if err != nil {
		return err
	}
	return f.api.CreateGitObjects(f.context(), inode, directory)
}

// Create Git Index based on given worktree dir
func (f *fsapi) CreateGitIndex(path, directory, workTree, hash string) error {
	// Add support
	return ErrNotSupported
}
func (f *fsapi) CreateGitIndexFromTree(ctx context.Context, path, directory, worktree string, root gitindex.TreeDirectory) error {
	return ErrNotSupported
}
func (f *fsapi) WriteGitIndex(indexPath string, res *gitindex.Result) error {
	return ErrNotSupported
}

// Tap the input command
func (f *fsapi) Tap(cmd *exec.Cmd) (*fs.TapCmdResult, error) {
	return nil, ErrInvalid
}

// Tap the input command and resolve the files when complete
func (f *fsapi) TapResolved(cmd *exec.Cmd) (*fs.TapCmdResolvedResult, error) {
	return nil, ErrInvalid
}

// Status of FS
func (f *fsapi) Status() (*fs.Status, error) {
	return f.api.Status()
}
func (f *fsapi) ARISize(path string) (string, int64, error) {
	return "", 0, fmt.Errorf("Unsupported option")
}
func (f *fsapi) SetARISize(path, uri string, s int64) error {
	return fmt.Errorf("Unsupported option")
}

func (f *fsapi) BackendStat(path string) (*fs.BackendStat, error) {
	return nil, fmt.Errorf("Unsupported option")
}
