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

package ioctl

import (
	"fmt"
	"unsafe"
)

const (
	CasfsHashLen = 20

	CASFS_TYPE_REG             = 0
	CASFS_TYPE_REG_EXEC        = 1
	CASFS_TYPE_DIR             = 2
	CASFS_TYPE_COMMIT          = 3
	CASFS_TYPE_SYMLINK         = 4
	CASFS_TYPE_FIFO            = 5
	CASFS_TYPE_SOCKET          = 6
	CASFS_TYPE_DEEPTREE        = 7
	CASFS_TYPE_GIT_OBJECTS_DIR = 8
	CASFS_TYPE_GIT_OBJECT_DIR  = 9
	CASFS_TYPE_CTRL            = 10

	CASFS_FLAG_LOADED   = 1 << 0
	CASFS_FLAG_EXCLUDED = 1 << 1
	CASFS_FLAG_DEBUG    = 1 << 2
	CASFS_FLAG_HAS_ARI  = 1 << 3

	// Linux ioctl encoding constants
	_IOC_NRBITS   = 8
	_IOC_TYPEBITS = 8
	_IOC_SIZEBITS = 14
	_IOC_DIRBITS  = 2

	_IOC_NRSHIFT   = 0
	_IOC_TYPESHIFT = _IOC_NRSHIFT + _IOC_NRBITS
	_IOC_SIZESHIFT = _IOC_TYPESHIFT + _IOC_TYPEBITS
	_IOC_DIRSHIFT  = _IOC_SIZESHIFT + _IOC_SIZEBITS

	_IOC_READ  = 2
	_IOC_WRITE = 1

	casfsIoctlMagic = 'C'
	casfsIocReadDir = 2
)

type CasfsReadDirArgs struct {
	BufPtr       uint64
	BufSize      uint64
	BytesWritten uint64
	EntriesCount uint32
	HasMore      uint32
	Cookie       uint64
}

type CasfsDirEntryHeader struct {
	CtimeSec    uint32
	CtimeNsec   uint32
	MtimeSec    uint32
	MtimeNsec   uint32
	Size        uint64
	Ino         uint64
	Dev         uint32
	Mode        uint32
	Uid         uint32
	Gid         uint32
	Dirtiness   int32
	Type        uint8
	BackendType uint8
	Flags       uint16
	NameLen     uint16
	AriLen      uint16
	Sha         [CasfsHashLen]byte
	CommitSha   [CasfsHashLen]byte
	Pad         [12]uint8
}

const SizeOfCasfsDirEntryHeader = unsafe.Sizeof(CasfsDirEntryHeader{})

func CasfsIocReadDirEntries() uintptr {
	size := unsafe.Sizeof(CasfsReadDirArgs{})
	dir := _IOC_READ | _IOC_WRITE
	return (uintptr(dir) << _IOC_DIRSHIFT) |
		(uintptr(casfsIoctlMagic) << _IOC_TYPESHIFT) |
		(uintptr(casfsIocReadDir) << _IOC_NRSHIFT) |
		(size << _IOC_SIZESHIFT)
}

func TypeString(t uint8) string {
	switch t {
	case CASFS_TYPE_REG:
		return "REG"
	case CASFS_TYPE_REG_EXEC:
		return "EXEC"
	case CASFS_TYPE_DIR:
		return "DIR"
	case CASFS_TYPE_COMMIT:
		return "COMMIT"
	case CASFS_TYPE_SYMLINK:
		return "SYMLINK"
	case CASFS_TYPE_FIFO:
		return "FIFO"
	case CASFS_TYPE_SOCKET:
		return "SOCKET"
	case CASFS_TYPE_DEEPTREE:
		return "DEEPTREE"
	case CASFS_TYPE_GIT_OBJECTS_DIR:
		return "GIT_OBJS"
	case CASFS_TYPE_GIT_OBJECT_DIR:
		return "GIT_OBJ"
	case CASFS_TYPE_CTRL:
		return "CTRL"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", t)
	}
}
