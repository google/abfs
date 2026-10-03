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

package xattr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/xattr"

	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/fs/gitindex"
)

const (
	hashAttrName        = "casfs.hash"
	hashSizeAttrName    = "casfs.hash_size"
	ariSizeAttrName     = "casfs.ari_size"
	hashStatAttrName    = "casfs.hash_stat"
	backendStatAttrName = "casfs.backend_stat"
	dirListHashAttrName = "casfs.dir_list_hash"
	deepTreeAttrName    = "casfs.deeptree"
	ctrlAttrName        = "casfs.ctrl"
	debugAttrName       = "casfs.debug"
	dirtyAttrName       = "casfs.dirty"
	diagsAttrName       = "casfs.diags"
	loadedAttrName      = "casfs.loaded"
	excludeAttrName     = "casfs.exclude"
	fsStatusAttrName    = "casfs.fs_status"

	// The standard Git config we extracted from the kernel
	gitConfigString = `[core]
	compression = 0
	looseCompression = 0
	abbrev = no

[fetch]
	unpackLimit = 2147483647
[maintenance.loose-objects]
	auto = 0

[maintenance.incremental-repack]
	auto = 0
[casfs]
	noObjectCompress = true
	flatObjectPath = true
`
)

// Implement fs.FS via xattr
type xattrFs struct {
	hasher    fs.Hasher
	mountPath string
	tapPath   string
}

const max_tries = 100
const wait_time_ms = 100 * time.Millisecond

func NewCASFS(mountPath string, hasher fs.Hasher) fs.FS {
	return &xattrFs{hasher: hasher, mountPath: mountPath}
}

// Need to bind tap path as for tapping as we don't want tap path
// as part of the api interface
func SetTapPath(f fs.FS, tapPath string) {
	f.(*xattrFs).tapPath = tapPath
}
func (xfs *xattrFs) MountPath() string {
	return xfs.mountPath
}
func (xfs *xattrFs) readString(path, attr string) (string, error) {
	data, err := xattr.LGet(path, attr)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
func (xfs *xattrFs) writeString(path, attr, val string) error {
	var err error
	for i := 0; i < max_tries; i++ {
		err = xattr.LSet(path, attr, []byte(val))
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EBUSY) && !errors.Is(err, syscall.ENOMEM) {
			return err
		}
		time.Sleep(wait_time_ms)
	}
	return err
}

func (xfs *xattrFs) readBool(path, attr string) (bool, error) {
	val, err := xfs.readString(path, attr)
	if err != nil {
		return false, err
	}
	if val == "0" {
		return false, nil
	}
	if val == "1" {
		return true, nil
	}
	return false, fmt.Errorf("invalid attr value %s", val)
}
func (xfs *xattrFs) writeBool(path, attr string, val bool) error {
	var valString string
	if val {
		valString = "1"
	} else {
		valString = "0"
	}
	return xfs.writeString(path, attr, valString)

}
func (xfs *xattrFs) readHash(path, attr string) (fs.Hash, error) {
	val, err := xfs.readString(path, attr)
	if err != nil {
		return nil, err
	}
	return xfs.hasher.NewHash(val)
}
func (xfs *xattrFs) writeHash(path, attr string, h fs.Hash) error {
	return xfs.writeString(path, attr, h.String())
}
func (xfs *xattrFs) readHashSize(path, attr string) (fs.Hash, int64, error) {

	val, err := xfs.readString(path, attr)
	if err != nil {
		return nil, 0, err
	}
	parts := strings.SplitN(val, ":", 2)
	if len(parts) != 2 {
		return nil, 0, fmt.Errorf("invalid hash size string %s", val)
	}

	h, err := xfs.hasher.NewHash(parts[0])
	if err != nil {
		return nil, 0, err
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, 0, err
	}
	return h, size, nil
}
func (xfs *xattrFs) readHashStat(path, attr string) (*fs.HashStat, error) {
	val, err := xfs.readString(path, attr)
	if err != nil {
		return nil, err
	}
	// Split hash:size:mode
	parts := strings.SplitN(val, ":", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid hash stat string %s", val)
	}
	h, err := xfs.hasher.NewHash(parts[0])
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, err
	}
	mode, err := strconv.ParseInt(parts[2], 10, 32)
	if err != nil {
		return nil, err
	}
	out := &fs.HashStat{Hash: h,
		Size: size,
		Mode: fs.OSModeToFSMode(uint32(mode))}

	return out, nil
}
func (xfs *xattrFs) writeHashSize(path, attr string, h fs.Hash, size int64) error {
	return xfs.writeString(path, attr, fmt.Sprintf("%s:%d", h.String(), size))
}

func (fs *xattrFs) readARISize(path, attr string) (string, int64, error) {
	val, err := fs.readString(path, attr)
	if err != nil {
		return "", 0, err
	}

	// Split from the rightmost colon so the ARI string can safely contain colons
	idx := strings.LastIndex(val, ":")
	if idx == -1 || idx == 0 || idx == len(val)-1 {
		return "", 0, fmt.Errorf("invalid ari size string %s", val)
	}

	ari := val[:idx]
	sizeStr := val[idx+1:]

	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("cannot parse size %q in %s: %w", sizeStr, val, err)
	}

	return ari, size, nil
}

func (xfs *xattrFs) readBackendStat(path, attr string) (*fs.BackendStat, error) {
	val, err := xfs.readString(path, attr)
	if err != nil {
		return nil, err
	}

	// Format: "ari:<ARI_STRING>:<SIZE>:<MODE>" or "hash:<HEX_HASH>:<SIZE>:<MODE>"
	// Since <ARI_STRING> can contain ':', we extract type from prefix and size/mode from the right.
	parts := strings.Split(val, ":")
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid backend stat string %s", val)
	}

	statType := parts[0]
	rawModeStr := parts[len(parts)-1]
	sizeStr := parts[len(parts)-2]

	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("cannot parse size %q: %w", sizeStr, err)
	}

	rawMode, err := strconv.ParseInt(rawModeStr, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("cannot parse mode %q: %w", rawModeStr, err)
	}
	mode := fs.OSModeToFSMode(uint32(rawMode))

	// Everything between the first ':' and second-to-last ':' is the payload (ARI or HASH)
	payload := strings.Join(parts[1:len(parts)-2], ":")

	switch statType {
	case "ari":
		return &fs.BackendStat{
			IsARI: true,
			ARI:   payload,
			Mode:  mode,
			Size:  size,
		}, nil
	case "hash":
		h, err := xfs.hasher.NewHash(payload)
		if err != nil {
			return nil, err
		}
		return &fs.BackendStat{
			IsARI: false,
			Hash:  h,
			Mode:  mode,
			Size:  size,
		}, nil
	default:
		return nil, fmt.Errorf("unknown backend stat type %s", statType)
	}
}

func (fs *xattrFs) writeARISize(path, attr, ari string, size int64) error {
	return fs.writeString(path, attr, fmt.Sprintf("%s:%d", ari, size))
}
func (xfs *xattrFs) Dirty(path string) (bool, error) {
	return xfs.readBool(path, dirtyAttrName)
}
func (xfs *xattrFs) DirList(path string) (fs.Hash, error) {
	return xfs.readHash(path, dirListHashAttrName)
}
func (xfs *xattrFs) Diags(path string) (string, error) {
	return xfs.readString(path, diagsAttrName)
}
func (xfs *xattrFs) Loaded(path string) (bool, error) {
	return xfs.readBool(path, loadedAttrName)
}
func (xfs *xattrFs) SetLoaded(path string, value bool) error {
	return xfs.writeBool(path, loadedAttrName, value)
}

func (xfs *xattrFs) Excluded(path string) (bool, error) {
	return xfs.readBool(path, excludeAttrName)
}

func (xfs *xattrFs) SetExcluded(path string, val bool) error {
	return xfs.writeBool(path, excludeAttrName, val)
}

func (xfs *xattrFs) Hash(path string) (fs.Hash, error) {
	return xfs.readHash(path, hashAttrName)
}
func (xfs *xattrFs) SetHash(path string, h fs.Hash) error {
	return xfs.writeHash(path, hashAttrName, h)
}

func (xfs *xattrFs) HashSize(path string) (fs.Hash, int64, error) {
	return xfs.readHashSize(path, hashSizeAttrName)
}
func (xfs *xattrFs) SetHashSize(path string, h fs.Hash, s int64) error {
	return xfs.writeHashSize(path, hashSizeAttrName, h, s)
}
func (xfs *xattrFs) HashStat(path string) (*fs.HashStat, error) {
	return xfs.readHashStat(path, hashStatAttrName)
}
func (xfs *xattrFs) DeepTree(path string) (fs.Hash, error) {
	return xfs.readHash(path, deepTreeAttrName)
}
func (xfs *xattrFs) SetDeepTree(path string, h fs.Hash) error {
	return xfs.writeHash(path, deepTreeAttrName, h)
}
func (fs *xattrFs) ARISize(path string) (string, int64, error) {
	return fs.readARISize(path, ariSizeAttrName)
}
func (fs *xattrFs) SetARISize(path, ari string, s int64) error {
	return fs.writeARISize(path, ariSizeAttrName, ari, s)
}
func (fs *xattrFs) BackendStat(path string) (*fs.BackendStat, error) {
	return fs.readBackendStat(path, backendStatAttrName)
}
func (xfs *xattrFs) Debug(path string) (bool, error) {
	return xfs.readBool(path, debugAttrName)
}
func (xfs *xattrFs) SetDebug(path string, val bool) error {
	return xfs.writeBool(path, debugAttrName, val)
}
func (xfs *xattrFs) CreateControl(path, directory string) error {
	return xfs.writeString(path, ctrlAttrName, fmt.Sprintf("ctrl:%s", directory))
}
func (xfs *xattrFs) CreateGitObjects(path, directory string) error {
	return xfs.writeString(path, ctrlAttrName, fmt.Sprintf("gitobj:%s", directory))
}

func (xfs *xattrFs) CreateGit(path, directory, head string) error {

	gitPath := filepath.Join(path, directory)

	if err := os.MkdirAll(gitPath, 0755); err != nil {
		return fmt.Errorf("failed to create base git directory: %w", err)
	}
	// Mark git as excluded from parent
	if err := xfs.SetExcluded(gitPath, true); err != nil {
		return fmt.Errorf("failed to set git to excluded: %w", err)
	}
	if err := xfs.CreateGitObjects(gitPath, "objects"); err != nil {
		return fmt.Errorf("failed to create intercepted objects dir: %w", err)
	}

	dirs := []string{
		filepath.Join(gitPath, "objects", "info"),
		filepath.Join(gitPath, "objects", "pack"),
		filepath.Join(gitPath, "refs"),
		filepath.Join(gitPath, "refs", "heads"),
		filepath.Join(gitPath, "refs", "tags"),
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to natively create directory %s: %w", d, err)
		}
	}

	headFile := filepath.Join(gitPath, "HEAD")
	if err := os.WriteFile(headFile, []byte(head+"\n"), 0644); err != nil {
		return fmt.Errorf("failed to write HEAD file: %w", err)
	}

	configFile := filepath.Join(gitPath, "config")
	if err := os.WriteFile(configFile, []byte(gitConfigString), 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	if err := xfs.CreateGitIndex(path, directory, path, head); err != nil {
		return fmt.Errorf("failed to build git index during project creation: %w", err)
	}

	return nil
}

func (xfs *xattrFs) WriteGitIndex(indexPath string, res *gitindex.Result) (retErr error) {
	lockPath := indexPath + ".lock"

	lf, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("index lock file %q already exists (another git process may be running, "+
				"or a stale lock remains from a crashed process): %w", lockPath, err)
		}
		return fmt.Errorf("failed to create index lock file %q: %w", lockPath, err)
	}

	// Only remove the lock on failure; a successful rename consumes it.
	defer func() {
		if retErr != nil {
			_ = os.Remove(lockPath)
		}
	}()

	// Exclude before writing any data so CASFS never hashes the transient index.
	if err := xfs.SetExcluded(lockPath, true); err != nil {
		_ = lf.Close()
		return fmt.Errorf("failed to exclude index lock file: %w", err)
	}

	if err := gitindex.WriteTo(lf, res); err != nil {
		_ = lf.Close()
		return fmt.Errorf("failed to write git index: %w", err)
	}

	if err := lf.Close(); err != nil {
		return fmt.Errorf("failed to close index lock file: %w", err)
	}

	// Exclusion is an inode attribute and survives the rename.
	if err := os.Rename(lockPath, indexPath); err != nil {
		return fmt.Errorf("failed to rename index lock file: %w", err)
	}

	future := time.Now().Add(1 * time.Second)
	if err := os.Chtimes(indexPath, future, future); err != nil {
		return fmt.Errorf("failed to bump index mtime: %w", err)
	}
	return nil
}

func (xfs *xattrFs) resolveGitIndexPaths(path, directory, worktree string) (gitDir, realWorkTree string, err error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}

	absWorkTree := absPath
	if worktree != "" {
		absWorkTree, err = filepath.Abs(worktree)
		if err != nil {
			return "", "", err
		}
	}

	if info, err := os.Stat(absWorkTree); err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("fatal: working tree '%s' does not exist", absWorkTree)
		}
		return "", "", fmt.Errorf("failed to stat working tree: %w", err)
	} else if !info.IsDir() {
		return "", "", fmt.Errorf("fatal: working tree '%s' is not a directory", absWorkTree)
	}

	gitDir = filepath.Join(absPath, directory)
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create base git directory: %w", err)
	}

	realWorkTree, err = filepath.EvalSymlinks(absWorkTree)
	if err != nil {
		realWorkTree = absWorkTree
	}
	return gitDir, realWorkTree, nil
}

func (xfs *xattrFs) CreateGitIndex(path, directory, worktree, hash string) error {
	gitDir, realWorkTree, err := xfs.resolveGitIndexPaths(path, directory, worktree)
	if err != nil {
		return err
	}

	headFile := filepath.Join(gitDir, "HEAD")
	if _, err := os.Stat(headFile); os.IsNotExist(err) {
		if err := os.WriteFile(headFile, []byte(hash+"\n"), 0644); err != nil {
			return fmt.Errorf("failed to write HEAD file: %w", err)
		}
	}

	scanResult, err := gitindex.Scan(realWorkTree, hash, 64*1024)
	if err != nil {
		return fmt.Errorf("failed to scan worktree: %w", err)
	}

	return xfs.WriteGitIndex(filepath.Join(gitDir, "index"), scanResult)
}

// CreateGitIndexFromTree builds the index from an initial TreeDirectory state.
// Unlike CreateGitIndex it does not write .git/HEAD: the caller supplies the
// tree directly rather than a commit-ish, so establishing HEAD is its
// responsibility.
func (xfs *xattrFs) CreateGitIndexFromTree(ctx context.Context, path, directory, worktree string, root gitindex.TreeDirectory) error {
	gitDir, realWorkTree, err := xfs.resolveGitIndexPaths(path, directory, worktree)
	if err != nil {
		return err
	}

	scanResult, err := gitindex.ScanTree(ctx, realWorkTree, root, 64*1024)
	if err != nil {
		return fmt.Errorf("failed to scan worktree with tree state: %w", err)
	}

	return xfs.WriteGitIndex(filepath.Join(gitDir, "index"), scanResult)
}

func (xfs *xattrFs) Tap(cmd *exec.Cmd) (*fs.TapCmdResult, error) {
	return RunTapCmd(cmd, xfs.hasher, xfs.tapPath, nil)
}
func (xfs *xattrFs) TapResolved(cmd *exec.Cmd) (*fs.TapCmdResolvedResult, error) {
	return RunTapCmdResolved(cmd, xfs, xfs.hasher, xfs.tapPath, nil)
}
func (xfs *xattrFs) Status() (*fs.Status, error) {
	ready, err := xfs.readBool(xfs.mountPath, fsStatusAttrName)
	if err != nil {
		return nil, err
	}
	return &fs.Status{Ready: ready}, nil
}
