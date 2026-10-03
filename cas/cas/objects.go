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

package cas

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/google/abfs/cas/hash"
)

type FileMode = fs.FileMode
type FileInfo = fs.FileInfo

var umask int

func init() {
	umask = syscall.Umask(0)
	syscall.Umask(umask)
}

// DirObjectExtType provides extension types on top of normal file system modes.
type DirObjectExtType int

const (
	DirObjectExtTypeNone      DirObjectExtType = iota // None
	DirObjectExtTypeSubModule                         // Submodule
	DirObjectExtTypeDeepTree                          // Deeptree

)

func (t DirObjectExtType) String() string {
	switch t {
	case DirObjectExtTypeNone:
		return ""
	case DirObjectExtTypeDeepTree:
		return "deeptree"
	case DirObjectExtTypeSubModule:
		return "submodule"
	default:
		return fmt.Sprintf("unknown extension type %d", t)
	}
}

// DirEntry represents a single entry within a directory.
type DirEntry struct {
	Hash    hash.Hash
	Size    int64
	Mode    FileMode
	ExtType DirObjectExtType
	Name    string
}

// DirListEntry represents a single listing within a directory
type DirListEntry struct {
	Name string
}

// Helpers to create common fs.FileModes with default permissions
func UserPerm(m FileMode) FileMode {
	return FileMode(int(m) & (^umask))
}
func FileModeFile() FileMode       { return UserPerm(0644) }
func FileModeExecutable() FileMode { return UserPerm(0755) }
func FileModeDir() FileMode        { return FileMode(os.ModeDir | UserPerm(os.ModePerm)) }
func FileModeSymLink() FileMode    { return FileMode(os.ModeSymlink | UserPerm(os.ModePerm)) }
func FileModeFifo() FileMode       { return FileMode(os.ModeNamedPipe | UserPerm(os.ModePerm)) }
func FileModeSocket() FileMode     { return FileMode(os.ModeSocket | UserPerm(os.ModePerm)) }

func FileModeIsSymLink(m FileMode) bool {
	return m&os.ModeSymlink != 0
}
func FileModeIsFifo(m FileMode) bool {
	return m&os.ModeNamedPipe != 0
}
func FileModeIsSocket(m FileMode) bool {
	return m&os.ModeSocket != 0
}
