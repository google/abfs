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
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"unsafe"

	"github.com/google/abfs/cas/fs/ioctl"
)

type IndexEntry struct {
	Path      string
	CtimeSec  uint32
	CtimeNsec uint32
	MtimeSec  uint32
	MtimeNsec uint32
	Dev       uint32
	Ino       uint32
	Mode      uint32
	Uid       uint32
	Gid       uint32
	Size      uint32
	Sha       [20]byte
}

type TreeEntry struct {
	Name         string
	EntryCount   int
	SubtreeCount int
	Sha          [20]byte
	Children     []*TreeEntry
}

func (t *TreeEntry) flatten(out *[]*TreeEntry) {
	*out = append(*out, t)
	for _, child := range t.Children {
		child.flatten(out)
	}
}

type Result struct {
	Entries []IndexEntry
	Trees   []*TreeEntry
}

func isSkipName(name string) bool {
	return name == "." || name == ".." || name == ".git"
}

func Scan(rootPath, rootHashHex string, bufSize int) (*Result, error) {
	if bufSize <= 0 {
		bufSize = 64 * 1024
	}

	res := &Result{
		Entries: make([]IndexEntry, 0),
		Trees:   make([]*TreeEntry, 0),
	}

	var rootSha [20]byte
	hasRootSha := false
	if len(rootHashHex) == 40 {
		if b, err := hex.DecodeString(rootHashHex); err == nil && len(b) == 20 {
			copy(rootSha[:], b)
			hasRootSha = true
		}
	}

	buf := make([]byte, bufSize)

	var walk func(fullPath, relPath, baseName string, dirSha [20]byte, isValidSha bool) (*TreeEntry, error)
	walk = func(fullPath, relPath, baseName string, dirSha [20]byte, isValidSha bool) (*TreeEntry, error) {
		f, err := os.Open(fullPath)
		if err != nil {
			return nil, err
		}

		closed := false
		defer func() {
			if !closed {
				_ = f.Close()
			}
		}()

		tree := &TreeEntry{
			Name:     baseName,
			Sha:      dirSha,
			Children: make([]*TreeEntry, 0),
		}
		if !isValidSha {
			tree.EntryCount = -1
		}

		type subDirInfo struct {
			fullPath string
			relPath  string
			name     string
			sha      [20]byte
		}
		var subdirs []subDirInfo

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
				return nil, fmt.Errorf("ioctl failed on %s: %w", fullPath, errno)
			}

			var offset uint64
			for offset < req.BytesWritten {
				if offset+uint64(ioctl.SizeOfCasfsDirEntryHeader) > req.BytesWritten {
					return nil, fmt.Errorf("corrupted entry in %s", fullPath)
				}

				var hdr ioctl.CasfsDirEntryHeader
				copy(
					unsafe.Slice((*byte)(unsafe.Pointer(&hdr)), unsafe.Sizeof(hdr)),
					buf[offset:offset+uint64(ioctl.SizeOfCasfsDirEntryHeader)],
				)

				nameStart := offset + uint64(ioctl.SizeOfCasfsDirEntryHeader)
				nameEnd := nameStart + uint64(hdr.NameLen)

				// Total record length = header + name + '\0' + ari + '\0'
				recEnd := nameEnd + 1 + uint64(hdr.AriLen) + 1
				if recEnd > req.BytesWritten {
					return nil, fmt.Errorf("entry exceeds buffer bounds in %s", fullPath)
				}

				name := string(buf[nameStart:nameEnd])
				offset = recEnd

				if isSkipName(name) {
					continue
				}

				// Skip ARI artifact references
				if hdr.Flags&ioctl.CASFS_FLAG_HAS_ARI != 0 || hdr.AriLen > 0 {
					continue
				}

				// Skip internal CASFS control and virtual object directories
				if hdr.Type == ioctl.CASFS_TYPE_CTRL ||
					hdr.Type == ioctl.CASFS_TYPE_GIT_OBJECTS_DIR ||
					hdr.Type == ioctl.CASFS_TYPE_GIT_OBJECT_DIR {
					continue
				}

				entryRelPath := name
				if relPath != "" {
					entryRelPath = relPath + "/" + name
				}

				switch hdr.Type {
				case ioctl.CASFS_TYPE_REG, ioctl.CASFS_TYPE_REG_EXEC, ioctl.CASFS_TYPE_SYMLINK, ioctl.CASFS_TYPE_COMMIT:
					entrySha := hdr.Sha

					var mode uint32
					switch hdr.Type {
					case ioctl.CASFS_TYPE_REG:
						mode = gitModeRegular
					case ioctl.CASFS_TYPE_REG_EXEC:
						mode = gitModeExecutable
					case ioctl.CASFS_TYPE_SYMLINK:
						mode = gitModeSymlink
					case ioctl.CASFS_TYPE_COMMIT:
						mode = gitModeSubmodule
						entrySha = hdr.CommitSha
					default:
						mode = hdr.Mode
					}

					res.Entries = append(res.Entries, IndexEntry{
						Path:      entryRelPath,
						CtimeSec:  hdr.CtimeSec,
						CtimeNsec: hdr.CtimeNsec,
						MtimeSec:  hdr.MtimeSec,
						MtimeNsec: hdr.MtimeNsec,
						Dev:       hdr.Dev,
						Ino:       uint32(hdr.Ino),
						Mode:      mode,
						Uid:       hdr.Uid,
						Gid:       hdr.Gid,
						Size:      uint32(hdr.Size),
						Sha:       entrySha,
					})
					if tree.EntryCount >= 0 {
						tree.EntryCount++
					}

				case ioctl.CASFS_TYPE_DIR:
					subdirs = append(subdirs, subDirInfo{
						fullPath: filepath.Join(fullPath, name),
						relPath:  entryRelPath,
						name:     name,
						sha:      hdr.Sha,
					})
				}
			}

			cookie = req.Cookie
			if req.HasMore == 0 {
				break
			}
		}

		// Close FD immediately after reading directory records
		_ = f.Close()
		closed = true

		zeroSha := [20]byte{}
		for _, sd := range subdirs {
			hasValidChildSha := sd.sha != zeroSha
			childTree, err := walk(sd.fullPath, sd.relPath, sd.name, sd.sha, hasValidChildSha)
			if err != nil {
				return nil, err
			}

			// Prune empty directories: Git does not track empty directories as tree objects
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

		// Discard empty subtrees; only root can persist with 0 entries
		if tree.EntryCount == 0 && baseName != "" {
			return nil, nil
		}

		return tree, nil
	}

	rootTree, err := walk(rootPath, "", "", rootSha, hasRootSha)
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
