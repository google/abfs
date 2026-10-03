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
	"io"
	"io/fs"
	"os/exec"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/fs/gitindex"
	"github.com/google/abfs/cas/hash"
)

type Hash = hash.Hash
type Hasher = hash.Hasher

// HashStat
type HashStat struct {
	Hash Hash
	Mode fs.FileMode
	Size int64
}

type BackendStat struct {
	IsARI bool
	ARI   string
	Hash  Hash
	Mode  fs.FileMode
	Size  int64
}

type Status struct {
	Ready bool // File system is fully mounted and can respond to queries
}

type FS interface {
	// Mount Path
	MountPath() string
	// Dirty check if the content associated with the path has been modified but not committed
	Dirty(path string) (bool, error)
	// Read the directory list hash at the given path, directory list contains only the
	// names of the entries within a directory.
	DirList(path string) (Hash, error)
	// Loaded checks it contents at the directory have been fetched locally to the filesystem.
	Loaded(path string) (bool, error)
	// Excluded checks if path is configured to be excluded from parent's hash calculation
	Excluded(path string) (bool, error)
	// SetExcluded change the excluded attribute of the path, marking the parent as dirty.
	SetExcluded(path string, val bool) error
	// Hash calculate (if dirty) and return the hash of the path.
	Hash(path string) (Hash, error)
	// SetHash set the hash of the path, only valid if the path is not yet loaded.
	SetHash(path string, h Hash) error
	// HashStat get the hash,mode, and size of the path
	HashStat(path string) (*HashStat, error)
	// HashSet get the hash,size of a file at the given path.
	HashSize(path string) (Hash, int64, error)
	// SetHashSize set the hash,size for a file at the given path, only valid if the path
	// is not yet loaded.
	SetHashSize(path string, h Hash, s int64) error
	// ARISize get the ARI string and size of the given path.
	ARISize(path string) (string, int64, error)
	// SetARISize set the ARI string and size for a file at the given path.
	SetARISize(path, uri string, s int64) error
	// BackendStat get either ARI or Hash along with size in a single call.
	BackendStat(path string) (*BackendStat, error)
	// SetDeepTree set the deep tree hash for the given directory path, only valid if
	// the path is not yet loaded.  After setting the deep tree hash all children
	// nodes can answer Stat() call without accessing the backend.
	SetDeepTree(path string, h Hash) error
	// Debug get the debug attribute of the path.
	Debug(path string) (bool, error)
	// SetDebug set dynamic debug for the given path
	SetDebug(path string, val bool) error

	// Create control directory under path
	CreateControl(path, directory string) error
	// Create Git project under given path
	CreateGit(path, directory, head string) error
	// Create Git objects only under the given path
	CreateGitObjects(path, directory string) error
	// Create Git Index based on given worktree dir
	CreateGitIndex(path, directory, workTree, hash string) error
	// CreateGitIndexFromTree builds the index from an initial TreeDirectory
	// state. Unlike CreateGitIndex it does not create .git/HEAD; the caller
	// is responsible for establishing a ref.
	CreateGitIndexFromTree(ctx context.Context, path, directory, worktree string, root gitindex.TreeDirectory) error

	// WriteGitIndex atomically writes res to indexPath via an excluded
	// lockfile, marks the result excluded, and bumps its mtime.
	WriteGitIndex(indexPath string, res *gitindex.Result) error

	// Tap the input command
	Tap(cmd *exec.Cmd) (*TapCmdResult, error)
	// Tap the input command and resolve the files when complete
	TapResolved(cmd *exec.Cmd) (*TapCmdResolvedResult, error)

	// Status of FS
	Status() (*Status, error)
}

// Prefer to use an object of FSFILE/FSAPI when interfacing with a CAS FS, this
// will allow in-memory versions to work and bypass making kernel calls
type FSFile interface {
	io.ReaderAt
	io.WriterAt
	io.Closer
}
type FSAPI interface {
	Getwd() (string, error)
	Chdir(path string) error
	Mkdir(path string, perm cas.FileMode) error
	MkdirAll(path string, perm cas.FileMode) error
	Remove(path string) error
	Rename(oldName, newName string) error
	Symlink(oldName, newName string) error
	Readlink(path string) (string, error)
	Link(oldName, newName string) error
	Mknod(path string, mode cas.FileMode, rDev int) error
	Lstat(path string) (cas.FileInfo, error)
	Stat(path string) (cas.FileInfo, error)
	Chmod(path string, mode cas.FileMode) error
	OpenFile(path string, flag int, perm cas.FileMode) (FSFile, error)
}
