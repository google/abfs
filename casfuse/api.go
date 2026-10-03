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

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/hash"
)

// Provide an internal CASFS API corresponding to the cas fs api using the internal inodes instead of paths
type API interface {
	Type(inode Inode) cas.FileMode
	// Check if the path has been modified locally and not committed to the store
	Dirty(inode Inode) (bool, error)
	// DirList get a directory listing hash
	DirList(ctx context.Context, inode Inode) (hash.Hash, error)
	// Loaded check if the content at the path has been loaded locally
	Loaded(inode Inode) (bool, error)
	// Excluded check if the path is excluded from parent's hash calculation
	Excluded(inode Inode) (bool, error)
	// SetExcluded sets the excluded attribute
	SetExcluded(inode Inode, val bool) error
	// Hash if dirty the contents are stored to the store with a new hash value
	Hash(ctx context.Context, inode Inode) (hash.Hash, error)
	// SetHash set the hash of the path, only valid if the path is not yet loaded.
	SetHash(inode Inode, hash hash.Hash) error
	// HashStat get the hash,mode, and size of the path
	HashStat(ctx context.Context, inode Inode) (*fs.HashStat, error)
	// HashSet get the hash,size of a file at the given path.
	HashSize(ctx context.Context, inode Inode) (hash.Hash, int64, error)
	// SetHashSize set the hash,size for a file at the given path, only valid if the path
	// is not yet loaded.
	SetHashSize(inode Inode, hash hash.Hash, size int64) error
	// SetDeepTree set the deep tree hash for the given directory path, only valid if
	// the path is not yet loaded.  After setting the deep tree hash all children
	// nodes can answer Stat() call without accessing the backend.
	SetDeepTree(inode Inode, hash hash.Hash) error
	// Debug get the debug attribute of the path.
	Debug(inode Inode) (bool, error)
	// SetDebug set dynamic debug for the given path
	SetDebug(inode Inode, val bool) error

	// Create control directory under path
	CreateControl(ctx context.Context, inode Inode, directory string) error
	// Create a non-cas ramfs type directory under the input path
	CreateRamfs(ctx context.Context, inode Inode, directory string) error
	// Create GIT project under given path
	CreateGit(ctx context.Context, inode Inode, directory, head string) error
	// Create GIT objects only under the given path
	CreateGitObjects(ctx context.Context, inode Inode, directory string) error
	// Status of FS
	Status() (*fs.Status, error)
}
