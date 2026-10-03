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
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/fs/gitindex"
	"github.com/google/abfs/cas/fs/xattr"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/cas/hash/hashers"
	"github.com/google/abfs/cas/hash/hashers/sha1"
	"github.com/prometheus/procfs"

	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

// Binding to underlying CAS filesystem, present a CAS FS/FSAPI interface for ABFS
// that will convert between ABFS objects (entities.Hash, entities.FileMode, entities.HashStat...)
// and the CAS filesystem.

const ControlDirName = ".ctrl"

type CASFS interface {
	Getwd() (string, error)
	Chdir(path string) error
	Mkdir(name string, m entities.FileMode) error
	MkdirAll(name string, m entities.FileMode) error
	Remove(path string) error
	Symlink(oldName, newName string) error
	Readlink(path string) (string, error)
	Link(oldName, newName string) error
	Mknod(path string, mode entities.FileMode, rDev int) error
	Lstat(path string) (cas.FileInfo, error)
	Stat(path string) (cas.FileInfo, error)
	Chmod(path string, mode cas.FileMode) error
	OpenFile(path string, flag int, mode entities.FileMode) (fs.FSFile, error)
	Dirty(path string) (bool, error)
	DirList(path string) (entities.Hash, error)
	Loaded(path string) (bool, error)
	Excluded(path string) (bool, error)
	SetExcluded(path string, val bool) error
	Hash(path string) (entities.Hash, error)
	SetHash(path string, h entities.Hash) error
	HashStat(path string) (*entities.HashStat, error)
	HashSize(path string) (entities.Hash, int64, error)
	SetHashSize(path string, h entities.Hash, s int64) error
	ARISize(path string) (string, int64, error)
	SetARISize(path, ari string, s int64) error
	BackendStat(path string) (*fs.BackendStat, error)
	SetDeepTree(path string, h entities.Hash) error
	Debug(path string) (bool, error)
	SetDebug(path string, val bool) error
	CreateControl(path string) error
	CreateGit(path, directory, head string) error
	CreateGitObjects(path, directory string) error
	CreateGitIndex(path, directory, workTree, hash string) error
	CreateGitIndexFromTree(ctx context.Context, path, directory, worktree string, root gitindex.TreeDirectory) error
	WriteGitIndex(indexPath string, res *gitindex.Result) error
	Tap(cmd *exec.Cmd) (*fs.TapCmdResult, error)
	TapResolved(cmd *exec.Cmd) (*fs.TapCmdResolvedResult, error)
	Status() (*fs.Status, error)
	SetDefaultTap()
}

type CASBackendStat = fs.BackendStat
type CASHashStat = fs.HashStat

type casfs struct {
	casapi fs.FS
	fsapi  fs.FSAPI
}

var _ CASFS = (*casfs)(nil)

func NewCASFS(casapi fs.FS, fsapi fs.FSAPI) CASFS {
	return &casfs{casapi: casapi, fsapi: fsapi}
}
func (c *casfs) Getwd() (string, error) {
	return c.fsapi.Getwd()
}
func (c *casfs) Chdir(path string) error {
	return c.fsapi.Chdir(path)
}

func (c *casfs) Mkdir(name string, m entities.FileMode) error {
	return c.fsapi.Mkdir(name, cas.FileMode(m))
}
func (c *casfs) MkdirAll(name string, m entities.FileMode) error {
	return c.fsapi.MkdirAll(name, cas.FileMode(m))
}
func (c *casfs) Remove(path string) error {
	return c.fsapi.Remove(path)
}
func (c *casfs) Symlink(oldName, newName string) error {
	return c.fsapi.Symlink(oldName, newName)
}
func (c *casfs) Readlink(path string) (string, error) {
	return c.fsapi.Readlink(path)
}
func (c *casfs) Link(oldName, newName string) error {
	return c.fsapi.Link(oldName, newName)
}
func (c *casfs) Mknod(path string, mode entities.FileMode, rDev int) error {
	return c.fsapi.Mknod(path, CASMode(mode), rDev)
}
func (c *casfs) Lstat(path string) (cas.FileInfo, error) {
	return c.fsapi.Lstat(path)
}
func (c *casfs) Stat(path string) (cas.FileInfo, error) {
	return c.fsapi.Stat(path)
}
func (c *casfs) Chmod(path string, mode cas.FileMode) error {
	return c.fsapi.Chmod(path, mode)
}
func (c *casfs) OpenFile(path string, flag int, mode entities.FileMode) (fs.FSFile, error) {
	return c.fsapi.OpenFile(path, flag, CASMode(mode))
}

// Dirty check if the content associated with the path has been modified but not committed
func (c *casfs) Dirty(path string) (bool, error) {
	return c.casapi.Dirty(path)
}

// Read the directory list hash at the given path, directory list contains only the
// names of the entries within a directory.
func (c *casfs) DirList(path string) (entities.Hash, error) {
	h, err := c.casapi.DirList(path)
	if err != nil {
		return entities.Hash{}, err
	}
	return ABFSHash(h), nil
}

// Loaded checks it contents at the directory have been fetched locally to the filesystem.
func (c *casfs) Loaded(path string) (bool, error) {
	return c.casapi.Loaded(path)
}

// Excluded checks if path is configured to be excluded from parent's hash calculation
func (c *casfs) Excluded(path string) (bool, error) {
	return c.casapi.Excluded(path)
}

// SetExcluded change the excluded attribute of the path, marking the parent as dirty.
func (c *casfs) SetExcluded(path string, val bool) error {
	return c.casapi.SetExcluded(path, val)
}

// Hash calculate (if dirty) and return the hash of the path.
func (c *casfs) Hash(path string) (entities.Hash, error) {
	h, err := c.casapi.Hash(path)
	if err != nil {
		return entities.Hash{}, err
	}
	return ABFSHash(h), nil
}

// SetHash set the hash of the path, only valid if the path is not yet loaded.
func (c *casfs) SetHash(path string, h entities.Hash) error {
	return c.casapi.SetHash(path, CASHash(&h))
}

// HashStat get the hash,mode, and size of the path
func (c *casfs) HashStat(path string) (*entities.HashStat, error) {
	hs, err := c.casapi.HashStat(path)
	if err != nil {
		return nil, err
	}

	return &entities.HashStat{
		Hash: ABFSHash(hs.Hash),
		Mode: ABFSMode(hs.Mode),
		Size: hs.Size}, nil

}

// HashSet get the hash,size of a file at the given path.
func (c *casfs) HashSize(path string) (entities.Hash, int64, error) {
	h, size, err := c.casapi.HashSize(path)
	if err != nil {
		return entities.Hash{}, 0, err
	}
	return ABFSHash(h), size, nil
}

// SetHashSize set the hash,size for a file at the given path, only valid if the path
// is not yet loaded.
func (c *casfs) SetHashSize(path string, h entities.Hash, s int64) error {
	return c.casapi.SetHashSize(path, CASHash(&h), s)
}

func (c *casfs) ARISize(path string) (string, int64, error) {
	return c.casapi.ARISize(path)
}

func (c *casfs) SetARISize(path, ari string, s int64) error {
	return c.casapi.SetARISize(path, ari, s)
}
func (c *casfs) BackendStat(path string) (*fs.BackendStat, error) {
	return c.casapi.BackendStat(path)
}

// SetDeepTree set the deep tree hash for the given directory path, only valid if
// the path is not yet loaded.  After setting the deep tree hash all children
// nodes can answer Stat() call without accessing the backend.
func (c *casfs) SetDeepTree(path string, h entities.Hash) error {
	return c.casapi.SetDeepTree(path, CASHash(&h))
}

// Debug get the debug attribute of the path.
func (c *casfs) Debug(path string) (bool, error) {
	return c.casapi.Debug(path)
}

// SetDebug set dynamic debug for the given path
func (c *casfs) SetDebug(path string, val bool) error {
	return c.casapi.SetDebug(path, val)
}

// Create control directory under path
func (c *casfs) CreateControl(path string) error {
	return c.casapi.CreateControl(path, ControlDirName)
}

// Create GIT project under given path
func (c *casfs) CreateGit(path, directory, head string) error {
	return c.casapi.CreateGit(path, directory, head)
}

// Create GIT objects only under the given path
func (c *casfs) CreateGitObjects(path, directory string) error {
	return c.casapi.CreateGitObjects(path, directory)
}

// Create GIT index
func (c *casfs) CreateGitIndex(path, directory, workTree, hash string) error {
	return c.casapi.CreateGitIndex(path, directory, workTree, hash)
}

// Create GIT index from starting root
func (c *casfs) CreateGitIndexFromTree(ctx context.Context, path, directory, worktree string, root gitindex.TreeDirectory) error {
	return c.casapi.CreateGitIndexFromTree(ctx, path, directory, worktree, root)
}

func (c *casfs) WriteGitIndex(indexPath string, res *gitindex.Result) error {
	return c.casapi.WriteGitIndex(indexPath, res)
}

// Tap the input command
func (c *casfs) Tap(cmd *exec.Cmd) (*fs.TapCmdResult, error) {
	return c.casapi.Tap(cmd)
}

// Tap the input command and resolve the files when complete
func (c *casfs) TapResolved(cmd *exec.Cmd) (*fs.TapCmdResolvedResult, error) {
	return c.casapi.TapResolved(cmd)
}

// Status of FS
func (c *casfs) Status() (*fs.Status, error) {
	return c.casapi.Status()
}
func (c *casfs) SetDefaultTap() {
	xattr.SetTapPath(c.casapi, path.Join(c.casapi.MountPath(), ControlDirName, "tap"))
}

// For most clients within ABFS provide a single interface to an external mounted
// CAS filesystem using OS calls to interface with.  Provided here as a convenience
// for clients.

func NewExternalCASFS(mountpoint string) CASFS {
	return NewCASFS(
		xattr.NewCASFS(mountpoint, hashers.CreateHasher(hashers.HasherTypeSHA1)),
		fs.NewOSFSAPI())
}

var gCas CASFS = NewExternalCASFS("")

func SetGlobalCASFS(fs CASFS) {
	gCas = fs
}

func Getwd() (string, error) {
	return gCas.Getwd()
}
func Chdir(path string) error {
	return gCas.Chdir(path)
}

func Mkdir(name string, m entities.FileMode) error {
	return gCas.Mkdir(name, m)
}
func MkdirAll(name string, m entities.FileMode) error {
	return gCas.MkdirAll(name, m)
}
func Remove(path string) error {
	return gCas.Remove(path)
}
func Symlink(oldName, newName string) error {
	return gCas.Symlink(oldName, newName)
}
func Readlink(path string) (string, error) {
	return gCas.Readlink(path)
}
func Link(oldName, newName string) error {
	return gCas.Link(oldName, newName)
}
func Mknod(path string, mode entities.FileMode, rDev int) error {
	return gCas.Mknod(path, mode, rDev)
}
func Lstat(path string) (cas.FileInfo, error) {
	return gCas.Lstat(path)
}
func Stat(path string) (cas.FileInfo, error) {
	return gCas.Stat(path)
}
func Chmod(path string, mode cas.FileMode) error {
	return gCas.Chmod(path, mode)
}
func OpenFile(path string, flag int, mode entities.FileMode) (fs.FSFile, error) {
	return gCas.OpenFile(path, flag, mode)
}
func Dirty(path string) (bool, error) {
	return gCas.Dirty(path)
}
func DirList(path string) (entities.Hash, error) {
	return gCas.DirList(path)
}
func Loaded(path string) (bool, error) {
	return gCas.Loaded(path)
}
func Excluded(path string) (bool, error) {
	return gCas.Excluded(path)
}
func SetExcluded(path string, val bool) error {
	return gCas.SetExcluded(path, val)
}
func Hash(path string) (entities.Hash, error) {
	return gCas.Hash(path)
}
func SetHash(path string, h entities.Hash) error {
	return gCas.SetHash(path, h)
}
func HashStat(path string) (*entities.HashStat, error) {
	return gCas.HashStat(path)
}
func HashSize(path string) (entities.Hash, int64, error) {
	return gCas.HashSize(path)
}

func SetHashSize(path string, h entities.Hash, s int64) error {
	return gCas.SetHashSize(path, h, s)
}

func ARISize(path string) (string, int64, error) {
	return gCas.ARISize(path)
}

func SetARISize(path, ari string, s int64) error {
	return gCas.SetARISize(path, ari, s)
}
func BackendStat(path string) (*fs.BackendStat, error) {
	return gCas.BackendStat(path)
}
func SetDeepTree(path string, h entities.Hash) error {
	return gCas.SetDeepTree(path, h)
}
func Debug(path string) (bool, error) {
	return gCas.Debug(path)
}

func SetDebug(path string, val bool) error {
	return gCas.SetDebug(path, val)
}

func CreateControl(path string) error {
	return gCas.CreateControl(path)
}

func CreateGit(path, directory, head string) error {
	return gCas.CreateGit(path, directory, head)
}

func CreateGitObjects(path, directory string) error {
	return gCas.CreateGitObjects(path, directory)
}

func CreateGitIndex(path, directory, workTree, hash string) error {
	return gCas.CreateGitIndex(path, directory, workTree, hash)
}
func CreateGitIndexFromTree(ctx context.Context, path, directory, worktree string, root gitindex.TreeDirectory) error {
	return gCas.CreateGitIndexFromTree(ctx, path, directory, worktree, root)
}
func WriteGitIndex(indexPath string, res *gitindex.Result) error {
	return gCas.WriteGitIndex(indexPath, res)
}
func Tap(cmd *exec.Cmd) (*fs.TapCmdResult, error) {
	return gCas.Tap(cmd)
}

func TapResolved(cmd *exec.Cmd) (*fs.TapCmdResolvedResult, error) {
	return gCas.TapResolved(cmd)
}

func Status() (*fs.Status, error) {
	return gCas.Status()
}

// Convert between ABFS/CAS
func CASHash(h *entities.Hash) hash.Hash {
	var out sha1.Sha1Hash
	copy(out[:], (*h)[:])
	return &out
}
func ABFSHash(h hash.Hash) entities.Hash {
	out := entities.Hash{}
	copy(out[:], h.Data())
	return out
}
func ABFSMode(m cas.FileMode) entities.FileMode {
	return entities.ModeToFileMode(m)

}
func CASMode(m entities.FileMode) cas.FileMode {
	out, _ := m.ToOSFileMode()
	return out
}
func DirEntry(de entities.FileEntry) cas.DirEntry {
	out := cas.DirEntry{
		Name: de.Name,
		Size: de.Size,
		Mode: CASMode(de.Mode),
		Hash: CASHash(&de.Hash),
	}
	switch de.Mode {
	case entities.FileModeSubmodule:
		out.ExtType = cas.DirObjectExtTypeSubModule
	case entities.FileModeDeepTree:
		out.ExtType = cas.DirObjectExtTypeDeepTree
	default:
		out.ExtType = cas.DirObjectExtTypeNone
	}
	return out
}
func CopyHashSize(srcName, dstName string) error {
	hash, size, err := HashSize(srcName)
	if err != nil {
		return err
	}
	return SetHashSize(dstName, hash, size)
}

// Where should these reside (addSlash/MountInfoForPath)?
func addSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

func MountInfoForPath(path string) (*procfs.Mount, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fs, err := procfs.NewFS("/proc")
	if err != nil {
		return nil, err
	}
	proc, err := fs.Self()
	if err != nil {
		return nil, err
	}
	mounts, err := proc.MountStats()
	if err != nil {
		return nil, err
	}

	var match *procfs.Mount
	for _, mnt := range mounts {
		if strings.HasPrefix(addSlash(path), addSlash(mnt.Mount)) {
			match = mnt
		}
	}
	if match == nil {
		return nil, fmt.Errorf("cannot find the mount for %s", path)
	}
	return match, nil
}

// CheckMountPoint checks if the mount point is valid for mounting.
// A mount-point should:
// 1) Should exist if not force is true.
// 2) Should be empty.
// 3) Not be already on a CASFS file-system (fuse or kmod).
// 4) Not be the current working directory.
func CheckMountPoint(mountPoint string, force bool) error {
	ents, err := os.ReadDir(mountPoint)
	if err != nil {
		if entities.IsNotExist(err) {
			if !force {
				return clierr.CLIErr("The mount-point %q does not exist.", mountPoint)
			}
			if err := os.MkdirAll(mountPoint, 0755); err != nil {
				return clierr.CLIErr("Could not create the mount-point %q, err: %v.", mountPoint, err)
			}
		} else {
			return err
		}
	}
	if len(ents) > 0 {
		return clierr.CLIErr("The mount-point %q is not empty.", mountPoint)
	}

	mnt, err := MountInfoForPath(mountPoint)
	if err != nil {
		return err
	}
	if mnt.Type == "fuse.abfs" || mnt.Type == "casfs" {
		if force {
			return exec.Command("abfs", "unmount", "-k", mountPoint).Run()
		} else {
			return clierr.CLIErr("Cannot mount on %s mounted path. Try unmounting first: `%s unmount %s`", mnt.Type, os.Args[0], mountPoint)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return err
	}
	abfsMntPoint, err := filepath.Abs(mountPoint)
	if err != nil {
		return err
	}
	abfsMntPoint, err = filepath.EvalSymlinks(abfsMntPoint)
	if err != nil {
		return err
	}
	if abfsMntPoint == cwd {
		return clierr.CLIErr("Cannot use current working directory as a mount point.")
	}

	return nil
}
