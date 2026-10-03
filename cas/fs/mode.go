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
	"io/fs"
	"syscall"
)

func OSModeToFSMode(mode uint32) fs.FileMode {
	out := fs.FileMode(mode & 0777)
	switch mode & syscall.S_IFMT {
	case syscall.S_IFBLK:
		out |= fs.ModeDevice
	case syscall.S_IFCHR:
		out |= fs.ModeDevice | fs.ModeCharDevice
	case syscall.S_IFDIR:
		out |= fs.ModeDir
	case syscall.S_IFIFO:
		out |= fs.ModeNamedPipe
	case syscall.S_IFLNK:
		out |= fs.ModeSymlink
	case syscall.S_IFREG:
	case syscall.S_IFSOCK:
		out |= fs.ModeSocket
	}
	if mode&syscall.S_ISGID != 0 {
		out |= fs.ModeSetgid
	}
	if mode&syscall.S_ISUID != 0 {
		out |= fs.ModeSetuid
	}
	if mode&syscall.S_ISVTX != 0 {
		out |= fs.ModeSticky
	}
	return out
}

func FSModeToOSMode(mode fs.FileMode) int32 {
	out := int32(mode.Perm())

	if mode&fs.ModeDevice != 0 {
		out |= syscall.S_IFBLK
	}
	if mode&(fs.ModeDevice|fs.ModeCharDevice) != 0 {
		out |= syscall.S_IFCHR
	}
	if mode&fs.ModeDir != 0 {
		out |= syscall.S_IFDIR
	}
	if mode&fs.ModeNamedPipe != 0 {
		out |= syscall.S_IFIFO
	}
	if mode&fs.ModeSymlink != 0 {
		out |= syscall.S_IFLNK
	}
	if mode&fs.ModeType == 0 {
		out |= syscall.S_IFREG
	}
	if mode&fs.ModeSocket != 0 {
		out |= syscall.S_IFSOCK
	}
	if mode&fs.ModeSetgid != 0 {
		out |= syscall.S_ISGID
	}
	if mode&fs.ModeSetuid != 0 {
		out |= syscall.S_ISUID
	}
	if mode&fs.ModeSticky != 0 {
		out |= syscall.S_ISVTX
	}

	return out
}
