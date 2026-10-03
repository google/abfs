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

package entities

import (
	"io/fs"
	"os"

	"github.com/google/abfs/third_party/go-git/plumbing/filemode"
)

// FileMode is a Git file mode, extended with ABFS-specific modes (see
// FileModeDevNode and FileModeDeepTree) and a stat-operation bit. String,
// ToOSFileMode and FileModeNew delegate to the go-git code in
// third_party/go-git.
type FileMode uint32

// String returns m as a 7-digit, zero-padded octal number.
func (m FileMode) String() string {
	return filemode.FileMode(m).String()
}

func (m FileMode) IsFile() bool {
	switch m {
	case FileModeRegular, FileModeExecutable:
		return true
	default:
		return false
	}
}

func (m FileMode) IsFileOrSymlink() bool {
	switch m {
	case FileModeRegular, FileModeExecutable, FileModeSymlink:
		return true
	default:
		return false
	}
}

func (m FileMode) IsDir() bool {
	return m == FileModeDir
}

func (m FileMode) IsStat() bool {
	return m&PatchStatOperation != 0
}

func (m *FileMode) SetStat() {
	*m |= PatchStatOperation
}

func (m *FileMode) UnsetStat() {
	*m &= ^PatchStatOperation
}

// ToOSFileMode returns the os.FileMode to use for a file system entry with
// Git mode m, or an error if m has no file system equivalent.
func (m FileMode) ToOSFileMode() (os.FileMode, error) {
	return filemode.FileMode(m).ToOSFileMode()
}

func (m FileMode) ToLetter() string {
	switch m {
	case FileModeDir:
		return "d"
	case FileModeDir | PatchStatOperation:
		return "D"
	case FileModeRegular:
		return "f"
	case FileModeRegular | PatchStatOperation:
		return "F"
	case FileModeExecutable:
		return "x"
	case FileModeExecutable | PatchStatOperation:
		return "X"
	case FileModeSymlink:
		return "s"
	case FileModeSymlink | PatchStatOperation:
		return "X"
	case FileModeSubmodule:
		return "c"
	case FileModeSubmodule | PatchStatOperation:
		return "C"
	case FileModeDevNode:
		return "v"
	case FileModeDevNode | PatchStatOperation:
		return "V"
	default:
		return m.String()
	}
}

func FileModeFromLetter(s string) (FileMode, error) {
	switch s {
	case "d":
		return FileModeDir, nil
	case "D":
		return FileModeDir | PatchStatOperation, nil
	case "f":
		return FileModeRegular, nil
	case "F":
		return FileModeRegular | PatchStatOperation, nil
	case "x":
		return FileModeExecutable, nil
	case "X":
		return FileModeExecutable | PatchStatOperation, nil
	case "s":
		return FileModeSymlink, nil
	case "S":
		return FileModeSymlink | PatchStatOperation, nil
	case "c":
		return FileModeSubmodule, nil
	case "C":
		return FileModeSubmodule | PatchStatOperation, nil
	case "v":
		return FileModeDevNode, nil
	case "V":
		return FileModeDevNode | PatchStatOperation, nil
	default:
		return FileModeNew(s)
	}
}

// FileModeNew parses the octal string representation of a FileMode. On error
// it returns FileModeEmpty.
func FileModeNew(s string) (FileMode, error) {
	m, err := filemode.New(s)
	return FileMode(m), err
}

func ModeToFileMode(mode fs.FileMode) FileMode {
	switch {
	case mode.IsDir():
		return FileModeDir
	case mode.IsRegular():
		if mode.Perm()&0100 == 0 {
			return FileModeRegular
		} else {
			return FileModeExecutable
		}
	case mode&os.ModeSymlink != 0:
		return FileModeSymlink
	default:
		return FileModeDevNode
	}
}
