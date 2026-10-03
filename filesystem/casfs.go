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

package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/google/ninja-go/xxmap"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/breakerrgroup"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	rbemodel "github.com/google/abfs/shared/rbe/model"
	"github.com/google/abfs/store"
)

type OsMounter interface {
	DoMount(path string, m *MountInfo, cfg *entities.FSConfig, casStore entities.CASStore) (bindcas.CASFS, error)
	DoUnmount(path string) error
	DoServe() error
}

// Provide Mount via cas filesystem
type CasfsMount struct {
	MountInfo
	mountPoint  string
	store       entities.CASStore
	originalCWD string
	initConfig  *entities.FSConfig
	root        bindcas.Dir
	osMounter   OsMounter
	casfs       bindcas.CASFS

	digestMap        xxmap.StringMap[entities.SHA256]
	loadedTrees      entities.SyncHashSet
	loadedDigestMaps entities.SyncHashSet
}

func NewCasfsMount(osMounter OsMounter) *CasfsMount {
	return &CasfsMount{
		osMounter:        osMounter,
		digestMap:        xxmap.NewStringMap[entities.SHA256](),
		loadedTrees:      entities.NewHashSet(),
		loadedDigestMaps: entities.NewHashSet(),
	}
}

func (m *CasfsMount) SetInitConfig(c *entities.FSConfig) {
	m.initConfig = c
}

func (m *CasfsMount) Config() *entities.FSConfig {
	return m.initConfig
}

func (m *CasfsMount) Mount(ctx context.Context, mountPoint string, mntInfo MountInfo, store entities.CASStore) error {
	m.mountPoint = mountPoint
	m.store = store
	m.MountInfo = mntInfo

	casfs, err := m.osMounter.DoMount(mountPoint, &m.MountInfo, m.Config(), store)
	if err != nil {
		return err
	}
	m.casfs = casfs
	m.originalCWD, err = m.casfs.Getwd()
	if err != nil {
		return err
	}
	// Change dir to mount point
	if err = m.casfs.Chdir(m.mountPoint); err != nil {
		return err
	}

	m.root = bindcas.CreateRootDir(m.casfs, m.mountPoint)

	// Add control to mount
	return m.root.CreateControl(ctx)

}
func (m *CasfsMount) Unmount() {
	// Chdir back from mount-point
	if m.casfs != nil {
		_ = m.casfs.Chdir(m.originalCWD)
	}
	_ = m.osMounter.DoUnmount(m.mountPoint)
}

func (m *CasfsMount) Serve() error {
	return m.osMounter.DoServe()
}

func (m *CasfsMount) Hashes(ctx context.Context, goCount int64, prefix string, paths []string) ([]string, map[string]string) {
	result := make([]string, len(paths))
	errs := make(map[string]string)

	var err error
	var h entities.Hash
	for i, p := range paths {
		p = path.Join(prefix, p)
		if p == "" || p == "/" {
			p = "."
			// There was previously a "continue" here, but that would cause the result list
			// to not line up with the input list. Also, there shouldn't be an issue retrieving
			// the hash of the root. The fuse implementation supports this.
		}
		h, err = m.casfs.Hash(p)
		if err == nil {
			result[i] = h.String()
		} else {
			// Unknown file should just return empty hash
			if entities.IsNotExist(err) {
				result[i] = entities.EmptyFileHash.String()
				continue
			}
			result[i] = ""
			errs[p] = err.Error()
		}
	}
	return result, errs
}

func (m *CasfsMount) HashStats(ctx context.Context, goCount int64, prefix string, paths []string) ([]entities.HashStat, map[string]string) {
	result := make([]entities.HashStat, len(paths))
	errs := make(map[string]string)

	for i, p := range paths {
		p = path.Join(prefix, p)
		if p == "" || p == "/" {
			p = "."
		}

		hs, err := m.casfs.HashStat(p)
		if err != nil {
			// Unknown file should just return empty hash
			if errors.Is(err, entities.ErrEntryNotFound) || entities.IsNotExist(err) {
				result[i] = entities.HashStat{
					Hash: entities.EmptyFileHash,
					Mode: entities.FileModeEmpty,
					Size: 0,
				}
			} else {
				errs[p] = err.Error()
			}
			continue
		}
		result[i] = *hs

	}
	return result, errs
}

func isNotDirError(err error) bool {
	if pathError, ok := err.(*os.PathError); ok {
		if pathError.Err == syscall.ENOTDIR {
			return true
		}
	}
	return false
}

func IsGitPath(p string) bool {
	if strings.Contains(p, "/.git/") || strings.HasSuffix(p, "/.git") {
		return true
	}
	if strings.HasPrefix(p, ".repo/manifests.git") || strings.HasPrefix(p, ".repo/projects") || strings.HasPrefix(p, ".repo/project-objects") {
		return true
	}

	return false
}

func (m *CasfsMount) ignoreMatch(p string) bool {
	if strings.HasSuffix(p, ".path_interposer_origpath") {
		return true
	}

	if m.initConfig != nil && m.initConfig.IgnoreSoongEnvAvail && strings.HasSuffix(p, "/soong/soong.environment.available") {
		return true
	}

	if IsGitPath(p) {
		return true
	}

	return false
}

func (m *CasfsMount) Matches(ctx context.Context, goCount int64, chroot string, patch entities.Patch) error {

	matchFunc := func(p *entities.PatchEntry) error {
		if m.ignoreMatch(p.Path) {
			return nil
		}

		name := p.Path
		name = path.Join(chroot, name)

		if p.Mode&entities.FileModeDir == entities.FileModeDir {
			stat, err := m.casfs.Lstat(name)
			if entities.IsNotExist(err) || isNotDirError(err) {
				return NewMatchError("cannot find directory %s", name)
			}
			if err != nil {
				return err
			}
			if !stat.Mode().IsDir() {
				return NewMatchError("not a directory %s", name)
			}
			if !p.Hash.IsZero() {
				dirListHash, err := m.casfs.DirList(name)
				if err != nil {
					return err
				}
				if p.Hash != dirListHash {
					return NewMatchError("dir-list-hash is %s but expected %s for %s", dirListHash, p.Hash, name)
				}
			}
			return nil
		}

		fileHash, err := m.casfs.Hash(name)
		if err != nil {
			if !entities.IsNotExist(err) {
				return fmt.Errorf("failed to read xattr for %s (expected %s): %w", name, p.Hash, err)
			}
			return NewMatchError("cannot find file %s", name)
		}
		if p.Mode.IsStat() {
			// For stat entries we only check the existence.
			return nil
		}
		if p.Hash != fileHash {
			return NewMatchError("file hash is %s but expected %s for %s", fileHash, p.Hash, name)
		}
		return nil

	}

	limit := int(goCount)
	if len(patch) < limit {
		limit = len(patch)
	}

	if limit == 1 {
		for _, p := range patch {
			if p.Path == "" || p.Path == "." {
				continue
			}
			if err := matchFunc(p); err != nil {
				return err
			}
		}
		return nil
	} else {
		errGroup := breakerrgroup.New()
		errGroup.SetLimit(limit)
		for _, p := range patch {
			if p.Path == "" || p.Path == "." {
				continue
			}
			errGroup.Go(func() error {
				return matchFunc(p)
			})
		}
		return errGroup.Wait()
	}
}

func (m *CasfsMount) getSymlinkTarget(ctx context.Context, hash entities.Hash) (string, error) {
	content, err := store.ReadAllBlob(ctx, m.store, hash)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (m *CasfsMount) CreateFile(ctx context.Context, name string, mode entities.FileMode, hash entities.Hash, size int64, exists bool) error {
	switch mode {
	case entities.FileModeSymlink:
		// Read the data to recreate the symlink
		if target, err := m.getSymlinkTarget(ctx, hash); err != nil {
			return fmt.Errorf("failed to read symlink: %w", err)
		} else if err := m.casfs.Symlink(target, name); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("failed to create symlink to %s: %w", target, err)
			}
			newTarget, err := m.casfs.Readlink(name)
			if err != nil {
				return fmt.Errorf("failed to read existing symlink: %w", err)
			}
			if newTarget != target {
				return fmt.Errorf("existing symlink has a different target (%s)", newTarget)
			}
		}
	case entities.FileModeRegular, entities.FileModeExecutable:
		if !exists {
			if err := m.casfs.Mknod(name, mode, 0); err != nil {
				if !errors.Is(err, os.ErrExist) {
					return fmt.Errorf("failed to create file: %w", err)
				}
			}
		} else {
			if f, err := m.casfs.OpenFile(name, os.O_CREATE, mode); err != nil {
				return fmt.Errorf("failed to create file: %w", err)
			} else {
				if err := f.Close(); err != nil {
					return fmt.Errorf("failed to close file: %w", err)
				}
			}
		}
		if err := m.casfs.SetHashSize(name, hash, size); err != nil {
			return fmt.Errorf("failed to set hash: %w", err)
		}
	default:
		return fmt.Errorf("cannot create a file with type %s", mode)
	}
	return nil
}

func noExecMode(mode entities.FileMode) entities.FileMode {
	if mode == entities.FileModeExecutable {
		return entities.FileModeRegular
	}
	return mode
}
func (m *CasfsMount) AddEntry(ctx context.Context, name string, mode entities.FileMode, hash entities.Hash, size int64) error {
	if mode == entities.FileModeDir {
		if err := m.casfs.MkdirAll(name, 0755); err != nil {
			return err
		}
		if hash.IsZero() || hash == entities.EmptyDirHash {
			return nil
		}
		dirHash, err := m.casfs.Hash(name)
		if err != nil {
			return err
		}

		if hash == dirHash {
			return nil
		}

		if dirHash != entities.EmptyDirHash {
			return fmt.Errorf("cannot create a dir %s with hash %s because it already has content (hash=%s)", name, hash, dirHash)
		}
		//TODO use DeepTree to prefetch
		return m.casfs.SetHash(name, hash)
	}

	stat, err := m.casfs.Lstat(name)
	if err != nil && !entities.IsNotExist(err) {
		return fmt.Errorf("failed to stat file %s: %w", name, err)
	}
	if entities.IsNotExist(err) {
		if err := m.casfs.MkdirAll(path.Dir(name), 0755); err != nil {
			return err
		}

		if err := m.CreateFile(ctx, name, mode, hash, size, false); err != nil {
			return fmt.Errorf("failed to create %s with (%s %s size=%d): %w", name, hash, mode, size, err)
		}
		return nil
	}
	fileMode := entities.ModeToFileMode(stat.Mode())

	fileHash, err := m.casfs.Hash(name)
	if err != nil {
		return fmt.Errorf("failed to read xattr for existing %s (trying to set to %s): %w", name, hash, err)
	}

	if fileMode == entities.FileModeDir {
		return fmt.Errorf("cannot change the type of directory %s to %s", name, mode)
	}

	if hash == fileHash {
		if fileMode == mode {
			return nil
		} else if fileMode == entities.FileModeExecutable && mode == entities.FileModeRegular {
			return m.casfs.Chmod(name, stat.Mode()&^0111)
		} else if fileMode == entities.FileModeRegular && mode == entities.FileModeExecutable {
			return m.casfs.Chmod(name, stat.Mode()|0111)
		}
	}
	exists := true
	if noExecMode(fileMode) != noExecMode(mode) || fileMode == entities.FileModeSymlink {
		if err := m.casfs.Remove(name); err != nil && !entities.IsNotExist(err) {
			return fmt.Errorf("failed to remove %s: %v", name, err)
		}
		exists = false
	}

	if err := m.CreateFile(ctx, name, mode, hash, size, exists); err != nil {
		return fmt.Errorf("failed to recreate %s with (%s %s size=%d): %w", name, hash, mode, size, err)
	}
	return nil
}

func (m *CasfsMount) MatchAndPatch(ctx context.Context, goCount int64, chroot string, readPatch, writePatch entities.Patch, deletes entities.DeletePatch) error {
	if err := m.Matches(ctx, goCount, chroot, readPatch); err != nil {
		return err
	}

	// Deletes are sorted. So removing the deletes in reverse order make the files be removed before directory itself
	for i := len(deletes) - 1; i >= 0; i-- {
		err := m.casfs.Remove(path.Join(chroot, deletes[i]))
		if entities.IsNotExist(err) {
			continue
		}
	}

	applyFunc := func(p *entities.PatchEntry) error {
		if m.ignoreMatch(p.Path) {
			return nil
		}

		name := path.Join(chroot, p.Path)

		return m.AddEntry(ctx, name, p.Mode, p.Hash, p.Size)
	}

	limit := min(int(goCount), len(writePatch))

	if limit == 1 {
		for _, p := range writePatch {
			if err := applyFunc(p); err != nil {
				return err
			}
		}
		return nil
	} else {
		errGroup := breakerrgroup.New()
		errGroup.SetLimit(limit)
		for _, p := range writePatch {
			errGroup.Go(func() error {
				return applyFunc(p)
			})
		}

		return errGroup.Wait()
	}
}

func (m *CasfsMount) Chroot(ctx context.Context, p string) (bindcas.Dir, error) {
	ret, err := m.root.MkdirAll(ctx, p)
	if err != nil {
		return ret, err
	}

	// Create a control interface under the chroot
	err = ret.CreateControl(ctx)
	return ret, err
}

func (m *CasfsMount) createAndWriteFile(dir bindcas.Dir, name string, data []byte) (retErr error) {
	f, err := m.casfs.OpenFile(filepath.Join(dir.Path(), name), os.O_CREATE|os.O_WRONLY, entities.FileModeRegular)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(f, &retErr)

	_, err = f.WriteAt(data, 0)
	return err
}

func (m *CasfsMount) NewGitDir(ctx context.Context, root, dir bindcas.Dir, info ProjectInfo) error {
	var err error
	if info.Commit == nil {
		return fmt.Errorf("info.Commit must be non-nil")
	}

	// Create the git directory via attribute, this will result in
	// bindcas.GitDirName under the input directory path
	if err := dir.CreateGit(ctx, entities.Hash(info.Commit.Hash)); err != nil {
		return err
	}

	// Create an AppDir object for new git path
	gitDir := m.CreateDir(path.Join(dir.Path(), bindcas.GitDirName))
	c, err := ProjectGitConfig(info)
	if err != nil {
		return err
	}
	if err = m.createAndWriteFile(gitDir, "config", c); err != nil {
		return err
	}

	packsDir := m.CreateDir(path.Join(dir.Path(), bindcas.GitDirName, "objects", "pack"))
	if info.Packs != nil {
		levels := len(strings.Split(path.Clean(info.Project.GetPath()), string(os.PathSeparator)))
		prefix := path.Join(strings.Repeat("../", levels+3), casgit.RepoDirName, casgit.RepoPacksDir)

		for _, pack := range info.Packs.Packs {
			if _, err := packsDir.Symlink(ctx, pack.PackID+".pack", path.Join(prefix, pack.Content.String())); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if _, err := packsDir.Symlink(ctx, pack.PackID+".idx", path.Join(prefix, pack.Index.String())); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if m.PartialClone {
				if err := m.createAndWriteFile(packsDir, pack.PackID+".promisor", nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (m *CasfsMount) linkRepoHooks(ctx context.Context, gitdir bindcas.Dir, levels int) error {
	hookDir := m.CreateDir(path.Join(gitdir.Path(), "hooks"))
	symlinkVal := path.Join(strings.Repeat("../", levels+1), "repo/hooks/commit-msg")
	if _, err := hookDir.Symlink(ctx, "commit-msg", symlinkVal); err != nil {
		return err
	}

	return nil
}

func (m *CasfsMount) NewRepoObjDir(ctx context.Context, root, dir bindcas.Dir, name string, info ProjectInfo) error {
	if err := dir.CreateGitObjects(ctx, name); err != nil {
		return err
	}

	gitDir := m.CreateDir(path.Join(dir.Path(), name))
	c, err := ProjectGitConfig(info)
	if err != nil {
		return err
	}
	if err := m.createAndWriteFile(gitDir, "config", c); err != nil {
		return err
	}

	packsDir := m.CreateDir(path.Join(gitDir.Path(), "objects", "pack"))
	rel, err := filepath.Rel(casgit.RepoDirName, gitDir.Path())
	if err != nil {
		return err
	}
	levels := len(strings.Split(rel, string(os.PathSeparator)))
	prefix := path.Join(strings.Repeat("../", levels+2), casgit.RepoPacksDir)

	if info.Packs != nil {
		for _, pack := range info.Packs.Packs {
			if _, err := packsDir.Symlink(ctx, pack.PackID+".pack", path.Join(prefix, pack.Content.String())); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if _, err := packsDir.Symlink(ctx, pack.PackID+".idx", path.Join(prefix, pack.Index.String())); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if m.PartialClone {
				if err := m.createAndWriteFile(packsDir, pack.PackID+".promisor", nil); err != nil {
					return err
				}
			}
		}
	}

	dirs := []string{"refs", "hooks", "rr-cache"}
	for _, dir := range dirs {
		if _, err := gitDir.MkdirAll(ctx, dir); err != nil {
			return err
		}
	}

	return m.linkRepoHooks(ctx, gitDir, levels)
}

func (m *CasfsMount) Root() bindcas.Dir {
	return m.root
}
func (m *CasfsMount) Ready() {
}

func (m *CasfsMount) CASFS() bindcas.CASFS {
	return m.casfs
}
func (m *CasfsMount) CreateDir(path string) bindcas.Dir {
	return bindcas.CreateDir(m.casfs, nil, path)
}

func cleanRelativePath(p string) (string, error) {
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	if strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("invalid path: %s", p)
	}
	return p, nil
}

func digestToARI(d rbemodel.Digest) string {
	return fmt.Sprintf("rbe:%s:%d", d.SHA256, d.Size)
}

func ariToDigest(ari string) (rbemodel.Digest, error) {
	parts := strings.Split(ari, ":")
	if len(parts) != 3 || parts[0] != "rbe" {
		return rbemodel.Digest{}, fmt.Errorf("invalid rbe ari: %s", ari)
	}

	sha256 := parts[1]
	size, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return rbemodel.Digest{}, err
	}

	return rbemodel.Digest{
		SHA256: sha256,
		Size:   size,
	}, nil
}
func fiModeToRawMode(fiMode fs.FileMode) uint32 {
	var mode uint32 = uint32(fiMode.Perm())
	switch {
	case fiMode.IsDir():
		mode |= syscall.S_IFDIR
	case fiMode&os.ModeSymlink != 0:
		mode |= syscall.S_IFLNK
	case fiMode&os.ModeNamedPipe != 0:
		mode |= syscall.S_IFIFO
	case fiMode&os.ModeSocket != 0:
		mode |= syscall.S_IFSOCK
	case fiMode&os.ModeDevice != 0 && fiMode&os.ModeCharDevice != 0:
		mode |= syscall.S_IFCHR
	case fiMode&os.ModeDevice != 0:
		mode |= syscall.S_IFBLK
	default:
		mode |= syscall.S_IFREG
	}
	return mode
}
func osFileInfoToMode(fi fs.FileInfo) uint32 {
	if sysStat, ok := fi.Sys().(*syscall.Stat_t); ok {
		return sysStat.Mode
	}
	return fiModeToRawMode(fi.Mode())
}

func (m *CasfsMount) SetRBEDigest(ctx context.Context, p string, digest rbemodel.Digest, mode uint32) error {
	if mode&syscall.S_IFMT == 0 {
		return fmt.Errorf("mode %#o is not fully qualified; must include file type bits (e.g. S_IFREG 0100000)", mode)
	}
	if (mode & syscall.S_IFMT) == syscall.S_IFDIR {
		return errors.New("cannot set RBE digest on a directory; only file blobs are supported")
	}

	p, err := cleanRelativePath(p)
	if err != nil {
		return err
	}

	if dir := path.Dir(p); dir != "." && dir != "/" {
		if err := m.casfs.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create parent directories for %s: %w", p, err)
		}
	}

	create := false
	stat, err := m.casfs.Lstat(p)
	if err != nil {
		if entities.IsNotExist(err) {
			create = true
		} else {
			return fmt.Errorf("failed to stat %s: %w", p, err)
		}
	} else {
		existingMode := osFileInfoToMode(stat)
		if (existingMode & syscall.S_IFMT) == syscall.S_IFDIR {
			return fmt.Errorf("cannot overwrite existing directory %s with file", p)
		}

		dirty, err := m.casfs.Dirty(p)
		if err != nil {
			return fmt.Errorf("failed to check dirty state for %s: %w", p, err)
		}

		if dirty {
			if err := m.casfs.Remove(p); err != nil {
				return fmt.Errorf("cannot remove existing dirty file %s: %w", p, err)
			}
			create = true
		} else if (existingMode & 07777) != (mode & 07777) {
			if err := m.casfs.Chmod(p, os.FileMode(mode&07777)); err != nil {
				return fmt.Errorf("failed to chmod %s: %w", p, err)
			}
		}
	}

	if create {
		if err := m.casfs.Mknod(p, entities.FileMode(mode), 0); err != nil {
			return fmt.Errorf("failed to create file inode for %s: %w", p, err)
		}
	}

	return m.casfs.SetARISize(p, digestToARI(digest), digest.Size)
}

func (m *CasfsMount) LoadTreeDigestMap(ctx context.Context, tree entities.Hash) error {
	if tree.IsZero() {
		return errors.New("tree hash is required")
	}

	if m.loadedTrees.Has(tree) {
		return nil
	}

	dmHash, err := binding.GetDigestMapResult(ctx, m.store, tree)
	if err != nil {
		if entities.IsNotExist(err) {
			return nil
		}
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	grp, ctx := errgroup.WithContext(ctx)
	grp.SetLimit(runtime.NumCPU())
	if err := m.loadDigestMap(ctx, dmHash, entities.NewHashSet(), grp); err != nil {
		// Drain the group of any tasks that may have been added before returning the error.
		cancel()
		_ = grp.Wait()
		return err
	}
	if err := grp.Wait(); err != nil {
		return err
	}

	m.loadedTrees.Put(tree)

	return nil
}

func (m *CasfsMount) loadDigestMap(ctx context.Context, dmHash entities.Hash, visited entities.SyncHashSet, grp *errgroup.Group) error {
	if visited.Has(dmHash) || m.loadedDigestMaps.Has(dmHash) {
		return nil
	}

	visited.Put(dmHash)

	dmBytes, err := m.store.GetObject(ctx, dmHash)
	if err != nil {
		return fmt.Errorf("failed to get DigestMap object %s: %w", dmHash, err)
	}
	dm := new(binding.DigestMap)
	if err := dm.Decode(dmHash, dmBytes); err != nil {
		return fmt.Errorf("failed to decode DigestMap %s: %w", dmHash, err)
	}

	m.AddDigestMapEntries(dm.Digests)

	for _, childHash := range dm.DigestMaps {
		f := func() error {
			return m.loadDigestMap(ctx, childHash, visited, grp)
		}

		// If we are here, grp is full, so we execute the function directly.
		if !grp.TryGo(f) {
			if err := f(); err != nil {
				return err
			}
		}
	}

	m.loadedDigestMaps.Put(dmHash)
	return nil
}

func (m *CasfsMount) AddDigestMapEntries(entries []binding.DigestEntry) {
	for _, e := range entries {
		m.digestMap.Put(e.Hash.String(), e.SHA256)
	}
}

func (m *CasfsMount) GetRBEDigest(ctx context.Context, p string) (*RBEPathStat, error) {
	p, err := cleanRelativePath(p)
	if err != nil {
		return nil, err
	}

	if resp, err := m.getRBEDigestFromCASFS(p); err != nil {
		return nil, err
	} else if resp != nil {
		return resp, nil
	}

	if resp, err := m.getRBEDigestFromDigestMap(p); err != nil {
		return nil, err
	} else if resp != nil {
		return resp, nil
	}

	// Fallback to manual calc
	return m.calculateRBEDigestFromDisk(p)
}

func (m *CasfsMount) getRBEDigestFromDigestMap(p string) (*RBEPathStat, error) {
	if m.casfs == nil {
		return nil, nil
	}

	// If the file is dirty (modified), we must not use the precomputed digest.
	dirty, err := m.casfs.Dirty(p)
	if err != nil {
		return nil, nil
	}
	if dirty {
		return nil, nil
	}

	h, size, err := m.casfs.HashSize(p)
	if err != nil || h.IsZero() {
		return nil, nil
	}

	sha256Hash := m.digestMap.Get(h.String())
	if sha256Hash.IsZero() {
		return nil, nil
	}

	stat, err := m.casfs.Lstat(p)
	if err != nil {
		return nil, fmt.Errorf("failed to lstat %s: %w", p, err)
	}
	mode := osFileInfoToMode(stat)
	if (mode & syscall.S_IFMT) == syscall.S_IFDIR {
		return nil, errors.New("cannot get digest of a directory; only file blobs and symlinks are supported")
	}

	return &RBEPathStat{
		Path: p,
		Digest: rbemodel.Digest{
			SHA256: sha256Hash.String(),
			Size:   size,
		},
		Mode: mode,
	}, nil
}

func (m *CasfsMount) getRBEDigestFromCASFS(p string) (*RBEPathStat, error) {
	// If the FS doesn't have an ARI, then don't treat this as an application
	// error.
	ari, _, err := m.casfs.ARISize(p)
	if err != nil || ari == "" {
		return nil, nil
	}

	digest, err := ariToDigest(ari)
	if err != nil {
		return nil, fmt.Errorf("could not convert ARI to digest: %w", err)
	}

	stat, err := m.casfs.Lstat(p)
	if err != nil {
		return nil, fmt.Errorf("failed to lstat %s: %w", p, err)
	}
	mode := osFileInfoToMode(stat)
	if (mode & syscall.S_IFMT) == syscall.S_IFDIR {
		return nil, errors.New("cannot get digest of a directory; only file blobs and symlinks are supported")
	}

	return &RBEPathStat{
		Path:   p,
		Digest: digest,
		Mode:   mode,
	}, nil
}

func (m *CasfsMount) saveLearnedDigest(p, sha256Hex string) {
	if m.casfs == nil {
		return
	}
	if dirty, err := m.casfs.Dirty(p); err == nil && !dirty {
		if gitHash, _, err := m.casfs.HashSize(p); err == nil && !gitHash.IsZero() {
			m.digestMap.Put(gitHash.String(), entities.NewSHA256(sha256Hex))
		}
	}
}

func (m *CasfsMount) calculateRBEDigestFromDisk(p string) (*RBEPathStat, error) {
	if m.mountPoint == "" {
		return nil, errors.New("mountPoint is empty")
	}

	fullPath := filepath.Join(m.mountPoint, p)
	f, err := os.OpenFile(fullPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			target, err := os.Readlink(fullPath)
			if err != nil {
				return nil, fmt.Errorf("failed to readlink %s: %w", p, err)
			}
			lstat, err := os.Lstat(fullPath)
			if err != nil {
				return nil, fmt.Errorf("failed to lstat %s: %w", p, err)
			}
			mode := osFileInfoToMode(lstat)
			h := sha256.Sum256([]byte(target))
			sha256Hex := hex.EncodeToString(h[:])
			stat := &RBEPathStat{
				Path: p,
				Digest: rbemodel.Digest{
					SHA256: sha256Hex,
					Size:   int64(len(target)),
				},
				Mode: mode,
			}
			m.saveLearnedDigest(p, sha256Hex)
			return stat, nil
		}
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	if fi.IsDir() {
		return nil, errors.New("cannot get digest of a directory; only file blobs and symlinks are supported")
	}

	if !fi.Mode().IsRegular() {
		return nil, errors.New("cannot get digest of a non-regular file")
	}

	mode := osFileInfoToMode(fi)

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}

	sha256Hex := hex.EncodeToString(h.Sum(nil))
	stat := &RBEPathStat{
		Path: p,
		Digest: rbemodel.Digest{
			SHA256: sha256Hex,
			Size:   fi.Size(),
		},
		Mode: mode,
	}
	m.saveLearnedDigest(p, sha256Hex)
	return stat, nil
}

func (m *CasfsMount) SetRBEDigests(ctx context.Context, goCount int64, items []RBEPathStat) map[string]string {
	errs := make(map[string]string)
	if len(items) == 0 {
		return errs
	}

	setItem := func(item RBEPathStat) (string, error) {
		if item.Path == "" {
			return "<empty>", errors.New("path is required")
		}
		if item.SHA256 == "" {
			return item.Path, errors.New("sha256 is required")
		}
		if err := m.SetRBEDigest(ctx, item.Path, item.Digest, item.Mode); err != nil {
			return item.Path, err
		}
		return "", nil
	}

	if goCount <= 1 {
		for _, item := range items {
			if k, err := setItem(item); err != nil {
				errs[k] = err.Error()
			}
		}
		return errs
	}

	limit := min(int(goCount), len(items))
	var mu sync.Mutex
	eg := breakerrgroup.New()
	eg.SetLimit(limit)

	for _, item := range items {
		it := item
		eg.Go(func() error {
			if k, err := setItem(it); err != nil {
				mu.Lock()
				errs[k] = err.Error()
				mu.Unlock()
			}
			return nil
		})
	}
	_ = eg.Wait()
	return errs
}

func (m *CasfsMount) GetRBEDigests(ctx context.Context, goCount int64, paths []string) ([]RBEPathStat, map[string]string) {
	result := make([]RBEPathStat, len(paths))
	errs := make(map[string]string)
	if len(paths) == 0 {
		return result, errs
	}

	getPath := func(p string) (*RBEPathStat, string, error) {
		if p == "" {
			return nil, "<empty>", errors.New("path is required")
		}
		resp, err := m.GetRBEDigest(ctx, p)
		if err != nil {
			return nil, p, err
		}
		return resp, "", nil
	}

	if goCount <= 1 {
		for i, p := range paths {
			resp, k, err := getPath(p)
			if err != nil {
				errs[k] = err.Error()
				continue
			}
			result[i] = *resp
		}
		return result, errs
	}

	limit := min(int(goCount), len(paths))
	var mu sync.Mutex
	eg := breakerrgroup.New()
	eg.SetLimit(limit)

	for i, p := range paths {
		idx, pathStr := i, p
		eg.Go(func() error {
			resp, k, err := getPath(pathStr)
			if err != nil {
				mu.Lock()
				errs[k] = err.Error()
				mu.Unlock()
				return nil
			}
			result[idx] = *resp
			return nil
		})
	}
	_ = eg.Wait()
	return result, errs
}
func (m *CasfsMount) BackendStatToRBE(ctx context.Context, p string, bStat bindcas.CASBackendStat) (*RBEPathStat, error) {
	p, err := cleanRelativePath(p)
	if err != nil {
		return nil, err
	}

	if bStat.IsARI && bStat.ARI != "" {
		d, err := ariToDigest(bStat.ARI)
		if err == nil {
			return &RBEPathStat{
				Path:   p,
				Digest: d,
				Mode:   fiModeToRawMode(bStat.Mode),
			}, nil
		}
	}
	if resp, err := m.getRBEDigestFromDigestMap(p); err != nil {
		return nil, err
	} else if resp != nil {
		return resp, nil
	}
	return m.calculateRBEDigestFromDisk(p)

}

func (m *CasfsMount) GetRBETapDigest(ctx context.Context, goCount int64, input RBETapDigestInput) (*RBETapDigestResponse, error) {
	limit := int(goCount)
	if limit < 1 {
		limit = 1
	}

	readKeys := make([]string, 0, len(input.Reads))
	for p, bStat := range input.Reads {
		if !bStat.Mode.IsDir() {
			readKeys = append(readKeys, p)
		}
	}
	sort.Strings(readKeys)

	writeKeys := make([]string, 0, len(input.Writes))
	for p := range input.Writes {
		writeKeys = append(writeKeys, p)
	}
	sort.Strings(writeKeys)

	resp := &RBETapDigestResponse{
		Reads:  make([]RBEPathStat, len(readKeys)),
		Writes: make([]RBEPathStat, len(writeKeys)),
	}

	egReads := breakerrgroup.New()
	egReads.SetLimit(limit)
	for idx, pathKey := range readKeys {
		i, p := idx, pathKey
		bStat := input.Reads[p]
		egReads.Go(func() error {
			stat, err := m.BackendStatToRBE(ctx, p, bStat)
			if err != nil {
				return err
			}
			resp.Reads[i] = *stat
			return nil
		})
	}
	if err := egReads.Wait(); err != nil {
		return nil, err
	}

	egWrites := breakerrgroup.New()
	egWrites.SetLimit(limit)
	for idx, pathKey := range writeKeys {
		i, p := idx, pathKey
		egWrites.Go(func() error {
			stat, err := m.GetRBEDigest(ctx, p)
			if err != nil {
				return err
			}
			resp.Writes[i] = *stat
			return nil
		})
	}
	if err := egWrites.Wait(); err != nil {
		return nil, err
	}

	return resp, nil
}
