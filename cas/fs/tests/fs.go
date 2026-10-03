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

package tests

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Test are run with umask=0...
const (
	DefaultDirMode     fs.FileMode = fs.FileMode(fs.ModeDir | 0777)
	DefaultFileMode    fs.FileMode = fs.FileMode(0666)
	DefaultExecMode    fs.FileMode = fs.FileMode(0777)
	DefaultSymlinkMode fs.FileMode = fs.FileMode(fs.ModeSymlink | 0777)
)

// Run stat on the input path validating the return values
func ValidateDir(path string) error {
	name := filepath.Base(path)
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Name() != name {
		return fmt.Errorf("name mismatch %s %s", fi.Name(), name)
	}

	if !fi.IsDir() {
		return fmt.Errorf("stat returned non dir")
	}
	return nil
}
func ValidateSymlink(sympath, target string) error {
	name := filepath.Base(sympath)
	fi, err := os.Lstat(sympath)
	if err != nil {
		return err
	}
	if fi.Name() != name {
		return fmt.Errorf("name mismatch %s %s", fi.Name(), name)
	}
	if fi.Size() != int64(len(target)) {
		return fmt.Errorf("size mismatch %d %d", fi.Size(), len(target))
	}
	return nil
}
func ValidateFile(path string, sz int64, mode fs.FileMode) error {
	name := filepath.Base(path)
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}

	if fi.Name() != name {
		return fmt.Errorf("name mismatch %s %s", fi.Name(), name)
	}
	if fi.IsDir() {
		return fmt.Errorf("stat returned dir")
	}
	if fi.Size() != sz {
		return fmt.Errorf("size mismatch %d %d", fi.Size(), sz)
	}
	if fi.Mode() != mode {
		return fmt.Errorf("mode mismatch file %o expected %o", fi.Mode(), mode)
	}
	return nil
}

func ValidateFileContent(path, content string) error {
	file, err := os.OpenFile(path, os.O_RDONLY, DefaultFileMode)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	if content != string(data) {
		return fmt.Errorf("data content mismatch")
	}
	return nil
}
func ValidateHardLink(p1, p2 string) error {
	// use lstat to test hardlinks to symlinks
	fi1, err := os.Lstat(p1)
	if err != nil {
		return err
	}
	fi2, err := os.Lstat(p2)
	if err != nil {
		return err
	}
	s1, ok := fi1.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("failed to get stat info for %s", p1)
	}
	s2, ok := fi2.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("failed to get stat info for %s", p2)
	}
	if *s1 != *s2 {
		return fmt.Errorf("stat mismatch between %s %s", p1, p2)
	}
	if s1.Nlink != 2 {
		return fmt.Errorf("expected 2 links to %s have %d instead", p1, s1.Nlink)
	}
	return nil
}

func MkdirTest(path string) error {
	err := os.Mkdir(path, DefaultDirMode)
	if err != nil {
		return err
	}
	return ValidateDir(path)
}

func RmdirTest(path string) error {
	if err := os.Mkdir(path, DefaultDirMode); err != nil {
		return err
	}
	if err := ValidateDir(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return nil
}
func FileTest(path string) error {
	mode := DefaultFileMode
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if err := ValidateFile(path, 0, mode); err != nil {
		return err
	}
	file.Close()
	if err := ValidateFile(path, 0, mode); err != nil {
		return err
	}
	return nil
}

func FileTestContent(path, content string) error {
	mode := DefaultFileMode
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	expLen := int64(len(content))
	if err := ValidateFile(path, 0, mode); err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		return err
	}

	if err := ValidateFile(path, expLen, mode); err != nil {
		return err
	}
	if err := ValidateFileContent(path, content); err != nil {
		return err
	}
	file.Close()
	if err := ValidateFile(path, expLen, mode); err != nil {
		return err
	}
	if err := ValidateFileContent(path, content); err != nil {
		return err
	}
	return nil
}
func FileTestTruncate(path, content string) error {
	if err := FileTestContent(path, content); err != nil {
		return err
	}
	if err := os.Truncate(path, 0); err != nil {
		return err
	}
	return nil
}
func FileTestTruncateOpen(path, content string) error {
	if err := FileTestContent(path, content); err != nil {
		return err
	}
	// Reopen the file via create
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	f.Close()
	if err := ValidateFileContent(path, ""); err != nil {
		return err
	}
	return nil

}
func FileTestContentAppend(path, content, append string) error {
	err := FileTestContent(path, content)
	if err != nil {
		return err
	}
	mode := DefaultFileMode
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	expContent := content + append
	expLen := int64(len(expContent))
	_, err = file.WriteString(append)
	if err != nil {
		return err
	}
	if err := ValidateFile(path, expLen, mode); err != nil {
		return err
	}
	if err := ValidateFileContent(path, expContent); err != nil {
		return err
	}
	file.Close()
	if err := ValidateFile(path, expLen, mode); err != nil {
		return err
	}
	if err := ValidateFileContent(path, expContent); err != nil {
		return err
	}
	return nil
}

func FileNotExistsTest(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, DefaultFileMode)
	if err == nil {
		f.Close()
		return fmt.Errorf("non-existing file opened")
	}
	return nil
}

// validate cannot write to read only file
func FileReadOnlyWrite(path string) error {
	err := FileTestContent(path, "read only data")
	if err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	if d, err := file.WriteString("test write of read only"); err == nil {
		return fmt.Errorf("wrote %d bytes to read only file", d)
	}
	return nil
}
func MknodFileTest(path string) error {
	mode := DefaultFileMode
	err := syscall.Mknod(path, syscall.S_IFREG|uint32(mode.Perm()), 0)
	if err != nil {
		return err
	}
	if err := ValidateFile(path, 0, mode); err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, mode)
	if err != nil {
		return err
	}

	content := "Test file data\n"
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	if err := ValidateFile(path, int64(len(content)), mode); err != nil {
		return err
	}
	if err := ValidateFileContent(path, content); err != nil {
		return err
	}

	file.Close()
	if err := ValidateFile(path, int64(len(content)), mode); err != nil {
		return err
	}
	if err := ValidateFileContent(path, content); err != nil {
		return err
	}
	return nil
}
func MknodFifoTest(path string) error {
	osMode := DefaultFileMode
	err := syscall.Mknod(path, syscall.S_IFIFO|uint32(osMode.Perm()), 0)
	if err != nil {
		return err
	}
	// add stat check
	return nil
}
func MknodSocketTest(path string) error {
	osMode := 0640
	err := syscall.Mknod(path, syscall.S_IFSOCK|uint32(osMode), 0)
	if err != nil {
		return err
	}
	// add stat check
	return nil
}

func SymlinkTest(target, symlink string) error {
	mode := DefaultFileMode
	if err := syscall.Mknod(target, syscall.S_IFREG|uint32(mode.Perm()), 0); err != nil {
		return err
	}
	if err := ValidateFile(target, 0, mode); err != nil {
		return err
	}
	if err := os.Symlink(target, symlink); err != nil {
		return err
	}
	return ValidateSymlink(symlink, target)
}

func SymlinkTestNoSource(target, symlink string) error {
	if err := os.Symlink(target, symlink); err != nil {
		return err
	}
	return ValidateSymlink(symlink, target)

}

func HardLinkFile(target, link string) error {
	if err := FileTestContent(target, "source file for target data"); err != nil {
		return err
	}
	if err := os.Link(target, link); err != nil {
		return err
	}
	// Validate the inode is the same between target and link and link count
	return ValidateHardLink(target, link)

}
func HardLinkFileDiffDir(d1, f1, d2, link string) error {
	// Create hard link for file in different directory
	if err := MkdirTest(d1); err != nil {
		return err
	}
	if err := MkdirTest(d2); err != nil {
		return err
	}
	fullFile := filepath.Join(d1, f1)
	fullLink := filepath.Join(d2, link)
	if err := FileTestContent(fullFile, "source file for target data"); err != nil {
		return err
	}
	if err := os.Link(fullFile, fullLink); err != nil {
		return err
	}
	// Validate the inode is the same between target and link and link count
	return ValidateHardLink(fullFile, fullLink)

}
func HardLinkFileAfterDelete(target, link string) error {
	if err := HardLinkFile(target, link); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		return err
	}
	fi, err := os.Stat(link)
	if err != nil {
		return err
	}
	s, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("failed to get stat info for %s", link)
	}
	if s.Nlink != 1 {
		return fmt.Errorf("expected 1 link to %s have %d instead", link, s.Nlink)
	}
	return nil

}
func HardLinkSymlink(target, symlink, link string) error {
	if err := SymlinkTest(target, symlink); err != nil {
		return err
	}
	if err := os.Link(symlink, link); err != nil {
		return err
	}
	// Validate the inode is the same between symlink and hard link and link count
	return ValidateHardLink(symlink, link)

}
func HardLinkSymlinkAfterDelete(target, symlink, link string) error {
	if err := HardLinkSymlink(target, symlink, link); err != nil {
		return err
	}
	if err := os.Remove(symlink); err != nil {
		return err
	}
	fi, err := os.Lstat(link)
	if err != nil {
		return err
	}
	s, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("failed to get stat info for %s", link)
	}
	if s.Nlink != 1 {
		return fmt.Errorf("expected 1 link to %s have %d instead", link, s.Nlink)
	}
	return nil

}
func HardLinkDir(target, link string) error {
	if err := MkdirTest(target); err != nil {
		return err
	}
	// We expect this to fail
	if err := os.Link(target, link); err == nil {
		return fmt.Errorf("hard link of directory did not fail")
	}
	return nil
}
func PopulateTestStage(path string) error {
	original, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(path); err != nil {
		return err
	}
	defer os.Chdir(original)

	for i := range 10 {
		npath := fmt.Sprintf("dir-%d", i)
		if err := MkdirTest(npath); err != nil {
			return err
		}
		fpath := filepath.Join(npath, "file")
		if err := FileTestContent(fpath, fpath); err != nil {
			return err
		}
		symlinkTarget := filepath.Join(npath, "symlink-target")
		symlink := filepath.Join(npath, "symlink")
		if err := SymlinkTest(symlinkTarget, symlink); err != nil {
			return err
		}
	}
	return nil
}

func ValidateTestStage(path string) error {
	original, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(path); err != nil {
		return err
	}
	defer os.Chdir(original)

	for i := range 10 {
		npath := fmt.Sprintf("dir-%d", i)
		if err := ValidateDir(npath); err != nil {
			return err
		}
		fpath := filepath.Join(npath, "file")
		if err := ValidateFileContent(fpath, fpath); err != nil {
			return err
		}
		symlinkTarget := filepath.Join(npath, "symlink-target")
		symlink := filepath.Join(npath, "symlink")
		if err := ValidateSymlink(symlink, symlinkTarget); err != nil {
			return err
		}
	}
	return nil
}

// Create a directory with a number of subdirectories/files/symlinks
func DirTestStage(path string) error {
	if err := MkdirTest(path); err != nil {
		return err
	}

	return PopulateTestStage(path)
}

func FileExecTest(path string) error {
	// Create file and then chmod to exec
	if err := MkdirTest(path); err != nil {
		return err
	}

	filename := filepath.Join(path, "file")
	if err := FileTest(filename); err != nil {
		return err
	}

	var mode fs.FileMode = 0200
	if err := os.Chmod(filename, mode); err != nil {
		return err
	}
	if err := ValidateFile(filename, 0, mode); err != nil {
		return err
	}
	mode = fs.FileMode(0640)
	if err := os.Chmod(filename, mode); err != nil {
		return err
	}
	if err := ValidateFile(filename, 0, mode); err != nil {
		return err
	}
	return nil
}
