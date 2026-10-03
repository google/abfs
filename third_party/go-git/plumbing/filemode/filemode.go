// Copyright 2018 Sourced Technologies, S.L., Google LLC
// SPDX-License-Identifier: Apache-2.0
// See the LICENSE file in third_party/go-git for the full license text.

package filemode

import (
	"fmt"
	"os"
	"strconv"
)

// A FileMode represents the kind of tree entries used by git. It
// resembles regular file systems modes, although FileModes are
// considerably simpler (there are not so many), and there are some,
// like Submodule that has no file system equivalent.
type FileMode uint32

const (
	// Empty is used as the FileMode of tree elements when comparing
	// trees in the following situations:
	//
	// - the mode of tree elements before their creation.  - the mode of
	// tree elements after their deletion.  - the mode of unmerged
	// elements when checking the index.
	//
	// Empty has no file system equivalent.  As Empty is the zero value
	// of FileMode, it is also returned by New and
	// NewFromOsNewFromOSFileMode along with an error, when they fail.
	Empty FileMode = 0
	// Dir represent a Directory.
	Dir FileMode = 0040000
	// Regular represent non-executable files.  Please note this is not
	// the same as golang regular files, which include executable files.
	Regular FileMode = 0100644
	// Deprecated represent non-executable files with the group writable
	// bit set.  This mode was supported by the first versions of git,
	// but it has been deprecated nowadays.  This library uses them
	// internally, so you can read old packfiles, but will treat them as
	// Regulars when interfacing with the outside world.  This is the
	// standard git behaviour.
	Deprecated FileMode = 0100664
	// Executable represents executable files.
	Executable FileMode = 0100755
	// Symlink represents symbolic links to files.
	Symlink FileMode = 0120000
	// Submodule represents git submodules.  This mode has no file system
	// equivalent.
	Submodule FileMode = 0160000
)

// New takes the octal string representation of a FileMode and returns
// the FileMode and a nil error.  If the string can not be parsed to a
// 32 bit unsigned octal number, it returns Empty and the parsing error.
//
// Example: "40000" means Dir, "100644" means Regular.
//
// Please note this function does not check if the returned FileMode
// is valid in git or if it is malformed.  For instance, "1" will
// return the malformed FileMode(1) and a nil error.
func New(s string) (FileMode, error) {
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return Empty, err
	}

	return FileMode(n), nil
}

// String returns the FileMode as a string in the standard git format,
// this is, an octal number padded with ceros to 7 digits.  Malformed
// modes are printed in that same format, for easier debugging.
//
// Example: Regular is "0100644", Empty is "0000000".
func (m FileMode) String() string {
	return fmt.Sprintf("%07o", uint32(m))
}

// ToOSFileMode returns the os.FileMode to be used when creating file
// system elements with the given git mode and a nil error on success.
//
// When the provided mode cannot be mapped to a valid file system mode
// (e.g.  Submodule) it returns os.FileMode(0) and an error.
//
// The returned file mode does not take into account the umask.
func (m FileMode) ToOSFileMode() (os.FileMode, error) {
	switch m {
	case Dir:
		return os.ModePerm | os.ModeDir, nil
	case Submodule:
		return os.ModePerm | os.ModeDir, nil
	case Regular:
		return os.FileMode(0644), nil
	// Deprecated is no longer allowed: treated as a Regular instead
	case Deprecated:
		return os.FileMode(0644), nil
	case Executable:
		return os.FileMode(0755), nil
	case Symlink:
		return os.ModePerm | os.ModeSymlink, nil
	}

	return os.FileMode(0), fmt.Errorf("malformed mode (%s)", m)
}
