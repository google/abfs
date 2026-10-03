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
	"errors"
	"os"
	"path"
	"regexp"

	"github.com/google/abfs/third_party/go-git/plumbing/filemode"
)

const ABFSSocketFileName = ".abfs.sock"

type EntryType int

const (
	EntryTypeDevNode = EntryType(iota)
	EntryTypeDir
	EntryTypeFile
	EntryTypeSymlink
)

func (e EntryType) String() string {
	switch e {
	case EntryTypeDevNode:
		return "Dev Node"
	case EntryTypeDir:
		return "Directory"
	case EntryTypeFile:
		return "File"
	case EntryTypeSymlink:
		return "Symlink"
	}
	return "Unknown"
}

func ModeToString(m FileMode) string {
	osMode, err := m.ToOSFileMode()
	if err != nil {
		return "??????????"
	}
	return osMode.String()
}

const OutDirVar = "OUT_DIR"

var OutDir = "out"

const DistDirVar = "DIST_DIR"

var DistDir = path.Join(OutDir, "dist")

const MismatchesLogFile = ".abfs_mismatches"

func init() {
	dir := os.Getenv(OutDirVar)
	if dir != "" {
		OutDir = path.Clean(dir)
	}
	dir = os.Getenv(DistDirVar)
	if dir != "" {
		DistDir = path.Clean(dir)
	}
}

type Sizer interface {
	Size() (size int64, err error)
}

type FSConfig struct {
	Debug       bool
	LogHttp     bool
	LogPathsStr []string         `json:"LogPaths"`
	LogPaths    []*regexp.Regexp `json:"-"`

	IgnoreSoongEnvAvail bool
}

const (
	// Git standard modes, as defined by go-git.
	FileModeEmpty      = FileMode(filemode.Empty)
	FileModeDir        = FileMode(filemode.Dir)
	FileModeRegular    = FileMode(filemode.Regular)
	FileModeDeprecated = FileMode(filemode.Deprecated)
	FileModeExecutable = FileMode(filemode.Executable)
	FileModeSymlink    = FileMode(filemode.Symlink)
	FileModeSubmodule  = FileMode(filemode.Submodule)

	// Modes after this are not git standard modes
	FileModeDevNode  FileMode = 0200000
	FileModeDeepTree FileMode = 0210000
)

var (
	ErrEntryNotFound       = errors.New("entry not found")
	ErrNotADirectory       = errors.New("entry is not a directory")
	ErrEntryADirectory     = errors.New("entry is a directory")
	ErrNotAFile            = errors.New("entry is not a file")
	ErrEntryExists         = errors.New("entry already exists")
	ErrNotADirectoryObject = errors.New("expected a directory object")
	ErrDirNotEmpty         = errors.New("directory not empty")
)

type DeepEntry struct {
	Name string
	Mode FileMode
	Hash Hash
	Size int64
}

type DeepTree struct {
	Entries []*DeepEntry
}

func IsDev() bool {
	return os.Getenv("ABFS_DEV") == "1"
}
