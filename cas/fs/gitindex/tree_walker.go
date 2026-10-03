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

package gitindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"unsafe"

	"github.com/google/abfs/cas/fs/ioctl"
)

// ScanTree walks the DeepTree as the authoritative source of truth,
// augmenting index entries with CASFS directory ioctl stat data where available.
func ScanTree(ctx context.Context, rootPath string, root TreeDirectory, bufSize int) (*Result, error) {
	if bufSize <= 0 {
		bufSize = 64 * 1024
	}

	res := &Result{
		Entries: make([]IndexEntry, 0),
		Trees:   make([]*TreeEntry, 0),
	}

	buf := make([]byte, bufSize)
	zeroSha := [20]byte{}

	// readFSDir reads all CASFS dentry records in one directory into a lookup map.
	// A nil map (with nil error) means "not present on disk as a directory".
	readFSDir := func(dirPath string) (map[string]ioctl.CasfsDirEntryHeader, error) {
		// O_DIRECTORY rejects a tree directory replaced by a regular file at
		// open time, which is what makes the ENOTDIR branch below sufficient.
		// O_NOFOLLOW is defensive: the subtree loop already invalidates on a
		// dir -> symlink type change, but following the link would let the
		// target's entries supply stat data for same-named tree paths.
		f, err := os.OpenFile(dirPath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
		if err != nil {
			if os.IsNotExist(err) ||
				errors.Is(err, syscall.ENOTDIR) ||
				errors.Is(err, syscall.ELOOP) {
				return nil, nil
			}
			return nil, err
		}
		defer f.Close()

		fsEntries := make(map[string]ioctl.CasfsDirEntryHeader)
		var cookie uint64

		for {
			req := ioctl.CasfsReadDirArgs{
				BufPtr:  uint64(uintptr(unsafe.Pointer(&buf[0]))),
				BufSize: uint64(len(buf)),
				Cookie:  cookie,
			}

			_, _, errno := syscall.Syscall(
				syscall.SYS_IOCTL,
				f.Fd(),
				ioctl.CasfsIocReadDirEntries(),
				uintptr(unsafe.Pointer(&req)),
			)
			if errno != 0 {
				return nil, fmt.Errorf("ioctl failed on %s: %w", dirPath, errno)
			}

			var offset uint64
			for offset < req.BytesWritten {
				if offset+uint64(ioctl.SizeOfCasfsDirEntryHeader) > req.BytesWritten {
					return nil, fmt.Errorf("corrupted entry in %s", dirPath)
				}

				var hdr ioctl.CasfsDirEntryHeader
				copy(
					unsafe.Slice((*byte)(unsafe.Pointer(&hdr)), unsafe.Sizeof(hdr)),
					buf[offset:offset+uint64(ioctl.SizeOfCasfsDirEntryHeader)],
				)

				nameStart := offset + uint64(ioctl.SizeOfCasfsDirEntryHeader)
				nameEnd := nameStart + uint64(hdr.NameLen)
				recEnd := nameEnd + 1 + uint64(hdr.AriLen) + 1
				if recEnd > req.BytesWritten {
					return nil, fmt.Errorf("entry exceeds buffer bounds in %s", dirPath)
				}

				name := string(buf[nameStart:nameEnd])
				offset = recEnd

				if isSkipName(name) {
					continue
				}
				if hdr.Type == ioctl.CASFS_TYPE_CTRL ||
					hdr.Type == ioctl.CASFS_TYPE_GIT_OBJECTS_DIR ||
					hdr.Type == ioctl.CASFS_TYPE_GIT_OBJECT_DIR {
					continue
				}

				fsEntries[name] = hdr
			}

			cookie = req.Cookie
			if req.HasMore == 0 {
				break
			}
		}
		return fsEntries, nil
	}

	// parentAbsent is set when an ancestor directory was missing, replaced by a
	// non-directory, or was a symlink. In that case the whole subtree is absent
	// on disk and no ioctl is issued: resolving fullPath would follow the
	// ancestor symlink and attribute the target's stat data to tree paths that
	// no longer exist.
	var walk func(treeDir TreeDirectory, fullPath, relPath string, parentAbsent bool) (*TreeEntry, error)
	walk = func(treeDir TreeDirectory, fullPath, relPath string, parentAbsent bool) (*TreeEntry, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		name := treeDir.Name()
		dirSha := treeDir.Sha()
		isValidSha := dirSha != zeroSha

		tree := &TreeEntry{
			Name:     name,
			Sha:      dirSha,
			Children: make([]*TreeEntry, 0),
		}
		if !isValidSha {
			tree.EntryCount = -1
		}

		// Read filesystem dentries for this directory via a single ioctl batch.
		// Skipped entirely when an ancestor is absent; fsEntries stays nil and
		// drives the "absent on disk" path below, so every tree entry beneath
		// is still emitted and reported deleted.
		var fsEntries map[string]ioctl.CasfsDirEntryHeader
		if !parentAbsent {
			var err error
			fsEntries, err = readFSDir(fullPath)
			if err != nil {
				return nil, err
			}
		} else {
			tree.EntryCount = -1
		}

		files, err := treeDir.Files(ctx)
		if err != nil {
			return nil, err
		}

		for _, fe := range files {
			entryRelPath := fe.Name
			if relPath != "" {
				entryRelPath = relPath + "/" + fe.Name
			}

			// Use deep tree as source of truth on mode/size/sha
			ie := IndexEntry{
				Path: entryRelPath,
				Mode: fe.Mode,
				Size: fe.Size,
				Sha:  fe.Sha,
			}

			if fsEntries != nil {
				if hdr, onDisk := fsEntries[fe.Name]; onDisk {
					if ie.Mode == gitModeSubmodule {
						// Git expects clean zeroed stat records for gitlinks/submodules
						ie.Dev = 0
						ie.Ino = 0
						ie.Uid = 0
						ie.Gid = 0
						ie.Size = 0
						ie.CtimeSec = 0
						ie.CtimeNsec = 0
						ie.MtimeSec = 0
						ie.MtimeNsec = 0
					} else {
						// Augment with device and inode specific metadata
						ie.Dev = hdr.Dev
						ie.Ino = uint32(hdr.Ino)
						ie.Uid = hdr.Uid
						ie.Gid = hdr.Gid
						ie.CtimeSec = hdr.CtimeSec
						ie.CtimeNsec = hdr.CtimeNsec
						ie.MtimeSec = hdr.MtimeSec
						ie.MtimeNsec = hdr.MtimeNsec

						// Check if the file has changed locally
						isDirty := (hdr.Dirtiness != 0)
						isHashMismatch := hdr.Sha != zeroSha && hdr.Sha != fe.Sha
						isAri := (hdr.Flags&ioctl.CASFS_FLAG_HAS_ARI != 0) || (hdr.AriLen > 0)

						if isDirty || isHashMismatch || isAri {
							// Mark cache-tree invalid for this directory and parents
							tree.EntryCount = -1

							// Zeroing out timestamps flags the entry as stat-dirty for Git,
							// forcing `git status` / `git diff` to inspect and show local modifications.
							ie.MtimeSec = 0
							ie.MtimeNsec = 0
						}
						// Compare file type before the exec bit: a regular
						// file replaced by a symlink (or vice versa) must
						// invalidate the cached tree, not just a mode flip.
						if hdr.Mode&modeTypeMask != fe.Mode&modeTypeMask {
							tree.EntryCount = -1
							ie.MtimeSec = 0
							ie.MtimeNsec = 0
						} else if fe.Mode == gitModeRegular || fe.Mode == gitModeExecutable {
							if (hdr.Mode&0111 != 0) != (fe.Mode == gitModeExecutable) {
								tree.EntryCount = -1
							}
						}
					}
					delete(fsEntries, fe.Name)
				} else {
					// File in tree but deleted on disk: cache is invalid
					tree.EntryCount = -1
				}
			} else {
				// Entire directory absent on disk
				tree.EntryCount = -1
			}

			res.Entries = append(res.Entries, ie)
			if tree.EntryCount >= 0 {
				tree.EntryCount++
			}
		}

		subtrees, err := treeDir.Subtrees(ctx)
		if err != nil {
			return nil, err
		}

		for _, sub := range subtrees {
			subName := sub.Name()
			subRelPath := subName
			if relPath != "" {
				subRelPath = relPath + "/" + subName
			}
			subFullPath := filepath.Join(fullPath, subName)

			// A directory in the tree may exist on disk as a file or symlink. That
			// is a modification, not an untracked entry, so consume it from
			// fsEntries but invalidate rather than letting it go unnoticed.
			childAbsent := fsEntries == nil
			if fsEntries != nil {
				if hdr, onDisk := fsEntries[subName]; onDisk {
					if hdr.Mode&modeTypeMask != modeTypeDir {
						tree.EntryCount = -1
						// Present but not a directory: everything the tree
						// expects beneath it is gone.
						childAbsent = true
					}
					delete(fsEntries, subName)
				} else {
					// In the tree, missing on disk.
					tree.EntryCount = -1
					childAbsent = true
				}

			}

			// Descend regardless: files beneath the replaced directory must remain
			// in the index so git reports them as deleted.
			childTree, err := walk(sub, subFullPath, subRelPath, childAbsent)
			if err != nil {
				return nil, err
			}
			if childTree == nil || childTree.EntryCount == 0 {
				continue
			}

			tree.Children = append(tree.Children, childTree)
			tree.SubtreeCount++
			if tree.EntryCount >= 0 {
				if childTree.EntryCount >= 0 {
					tree.EntryCount += childTree.EntryCount
				} else {
					tree.EntryCount = -1
				}
			}
		}

		// Untracked files on disk invalidate the cached tree
		if len(fsEntries) > 0 {
			tree.EntryCount = -1
		}

		if tree.EntryCount == 0 && name != "" {
			return nil, nil
		}
		return tree, nil
	}

	rootTree, err := walk(root, rootPath, "", false)
	if err != nil {
		return nil, err
	}
	if rootTree != nil {
		rootTree.flatten(&res.Trees)
	}

	sort.Slice(res.Entries, func(i, j int) bool {
		return res.Entries[i].Path < res.Entries[j].Path
	})

	return res, nil
}
