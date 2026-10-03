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
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/fs"
)

func statMatch(fi1, fi2 cas.FileInfo) error {
	if fi1.Size() != fi2.Size() {
		return fmt.Errorf("size mismatch %d %d", fi1.Size(), fi2.Size())
	}
	if fi1.Mode() != fi2.Mode() {
		return fmt.Errorf("mode mismatch %d %d", fi1.Mode(), fi2.Mode())
	}
	if fi1.ModTime() != fi2.ModTime() {
		return fmt.Errorf("modtime mismatch %s %s", fi1.ModTime(), fi2.ModTime())
	}
	if fi1.IsDir() != fi2.IsDir() {
		return fmt.Errorf("isdir mismatch %t %t", fi1.IsDir(), fi2.IsDir())
	}
	return nil
}
func APIValidate(fi cas.FileInfo, expName string, expMode cas.FileMode) error {
	if fi.Name() != expName {
		return fmt.Errorf("name mismatch %s %s", fi.Name(), expName)
	}
	if fi.Mode() != expMode {
		return fmt.Errorf("stat returned mode %s expected %s", fi.Mode(), expMode)
	}
	return nil
}
func APIValidateLstat(api fs.FSAPI, path string, expMode cas.FileMode) error {
	fi, err := api.Lstat(path)
	if err != nil {
		return fmt.Errorf("lstat path %s error : %v", path, err)
	}
	return APIValidate(fi, filepath.Base(path), expMode)
}
func APIValidateStat(api fs.FSAPI, path string, expName string, expMode cas.FileMode) error {
	fi, err := api.Stat(path)
	if err != nil {
		return err
	}
	return APIValidate(fi, expName, expMode)
}

// Mkdir
func APIMkdir(api fs.FSAPI, path string, mode cas.FileMode) error {
	if err := api.Mkdir(path, mode); err != nil {
		return err
	}
	return APIValidateLstat(api, path, mode)
}
func APIMkdirFail(api fs.FSAPI, path string, mode cas.FileMode) error {
	// create directory twice checking return code matches expected
	if err := api.Mkdir(path, mode); err != nil {
		return err
	}
	if err := api.Mkdir(path, mode); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("expected IsExists failure got %v", err)
	} else {
		return fmt.Errorf("expected failure on mkdir return success")
	}
}
func APIMkdirDepth(api fs.FSAPI, path string, count int) (string, error) {
	mode := cas.FileModeDir()
	for _ = range count {
		path = filepath.Join(path, "dir")
		if err := APIMkdir(api, path, mode); err != nil {
			return "", err
		}
	}
	return path, nil
}

func APIMkdirAll(api fs.FSAPI, path string, mode cas.FileMode) error {
	if err := api.MkdirAll(path, mode); err != nil {
		return err
	}
	return APIValidateLstat(api, path, mode)
}

// Remove
func APIRemove(api fs.FSAPI, path string) error {
	err := api.Remove(path)
	if err != nil {
		return err
	}
	_, err = api.Stat(path)
	if err == nil {
		return fmt.Errorf("stat returned success for deleted file")
	}
	return nil
}
func APIRemoveDoesNotExist(api fs.FSAPI, path string) error {
	if err := APIRemove(api, path); err != nil {
		if pathErr, ok := err.(*os.PathError); ok {
			if os.IsNotExist(pathErr) {
				return nil
			}
		}
		return fmt.Errorf("exptected not exist error got %v", err)
	}
	return fmt.Errorf("expected failure removing non-existing file")
}

func APIRemoveDirNotEmpty(api fs.FSAPI, path string) error {
	mode := cas.FileModeDir()
	if err := APIMkdirAll(api, path, mode); err != nil {
		return err
	}
	fpath := filepath.Join(path, "file")
	if err := APIMknod(api, fpath, cas.FileModeFile(), 0); err != nil {
		return err
	}
	if err := APIRemove(api, path); err != nil {
		if pathErr, ok := err.(*os.PathError); ok {
			if pathErr.Err == syscall.ENOTEMPTY {
				return nil
			}
		}
		return fmt.Errorf("expected not empty error got %v", err)
	} else {
		return fmt.Errorf("remove on directory did not fail while not empty")
	}

}

// Rename
func APIRename(api fs.FSAPI, source, target string) error {
	fi1, err := api.Stat(source)
	if err != nil {
		return err
	}
	err = api.Rename(source, target)
	if err != nil {
		return err
	}
	_, err = api.Stat(source)
	if err == nil {
		return fmt.Errorf("expected failure stating old name")
	}
	fi2, err := api.Stat(target)
	if err != nil {
		return err
	}
	return statMatch(fi1, fi2)
}
func APIRenameInvalidPath(api fs.FSAPI, source, target string) error {
	err := api.Rename(source, target)
	if err == nil {
		return fmt.Errorf("expected rename failure on invalid source")
	}
	if linkErr, ok := err.(*os.LinkError); ok {
		if os.IsNotExist(linkErr) {
			return nil
		}
		return fmt.Errorf("expected not exists error got %v", err)
	}
	return fmt.Errorf("expected link error got %v", err)
}

// Symlink
func APISymlinkToDir(api fs.FSAPI, path, target string) error {
	mode := cas.FileModeDir()
	if err := APIMkdirAll(api, path, mode); err != nil {
		return err
	}
	if err := api.Symlink(path, target); err != nil {
		return err
	}
	if err := APIValidateLstat(api, target, cas.FileModeSymLink()); err != nil {
		return err
	}
	return APIValidateStat(api, target, filepath.Base(target), mode)
}
func APISymlink(api fs.FSAPI, path, target string) error {
	if err := api.Symlink(path, target); err != nil {
		return err
	}
	if err := APIValidateLstat(api, target, cas.FileModeSymLink()); err != nil {
		return err
	}

	fi1, err := api.Stat(target)
	if err != nil {
		return err
	}
	return APIValidateStat(api, target, filepath.Base(target), fi1.Mode())
}
func APISymlinkExists(api fs.FSAPI, path, target string) error {
	if err := api.Symlink(path, target); err != nil {
		return err
	}
	if err := api.Symlink(path, target); err != nil {
		if linkErr, ok := err.(*os.LinkError); ok {
			if os.IsExist(linkErr) {
				return nil
			}
		}
		return fmt.Errorf("expected link error got %v", err)
	}
	return fmt.Errorf("expected existing symlink to fail")
}

// Readlink
func APIReadLink(api fs.FSAPI, path, target string) error {
	if err := api.Symlink(path, target); err != nil {
		return err
	}
	ln, err := api.Readlink(target)
	if err != nil {
		return err
	}
	if ln != path {
		return fmt.Errorf("target mismatch <%s> != <%s>", path, ln)
	}
	return nil
}
func APIReadLinkInvalid(api fs.FSAPI, path string) error {
	mode := cas.FileModeFile()
	err := api.Mknod(path, mode, 0)
	if err != nil {
		return err
	}
	_, err = api.Readlink(path)
	if err != nil {
		if pathErr, ok := err.(*os.PathError); ok {
			if pathErr.Unwrap() != os.ErrInvalid {
				return nil
			}
		}
		return fmt.Errorf("exptected not exist error got %v", err)
	}
	return fmt.Errorf("expected readlink error")

}

// Hardlink
func APILink(api fs.FSAPI, source, target string) error {
	if err := api.Link(source, target); err != nil {
		return err
	}
	fi, err := api.Lstat(source)
	if err != nil {
		return err
	}
	return APIValidateLstat(api, target, fi.Mode())

}

// Source is a symlink
func APILinkSymlink(api fs.FSAPI, source, target string) error {
	if err := api.Link(source, target); err != nil {
		return err
	}
	fi1, err := api.Stat(source)
	if err != nil {
		return err
	}
	fi2, err := api.Stat(target)
	if err != nil {
		return err
	}
	return statMatch(fi1, fi2)
}

// Mknod
func APIMknod(api fs.FSAPI, path string, mode cas.FileMode, rDev int) error {
	err := api.Mknod(path, mode, rDev)
	if err != nil {
		return err
	}
	return APIValidateLstat(api, path, mode)
}

// Openfile
func APIOpenFile(api fs.FSAPI, path string, flag int, mode cas.FileMode) (fs.FSFile, error) {
	out, err := api.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	if err := APIValidateLstat(api, path, mode); err != nil {
		return nil, err
	}
	return out, nil
}

// Create write close read close tests
func APICreateTest(api fs.FSAPI, fpath string) error {
	flag := os.O_CREATE | os.O_WRONLY
	mode := cas.FileModeFile()
	f, err := APIOpenFile(api, fpath, flag, mode)
	if err != nil {
		return fmt.Errorf("failed to open file for write: %v", err)
	}

	testData := "test data "
	sz, err := f.WriteAt([]byte(testData), 0)
	if err != nil {
		return fmt.Errorf("failed to write data: %v", err)
	}
	if sz != len(testData) {
		return fmt.Errorf("wrote %d bytes expected %d", sz, len(testData))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("error closing write file: %v", err)
	}

	flag = os.O_RDONLY
	f, err = APIOpenFile(api, fpath, flag, mode)
	if err != nil {
		return fmt.Errorf("failed to open file for read: %v", err)
	}
	data := make([]byte, sz)

	rdSz, err := f.ReadAt(data, 0)
	if err != nil {
		return fmt.Errorf("failed to read data: %v", err)
	}
	if rdSz != sz {
		return fmt.Errorf("read size %d not expected %d", rdSz, sz)
	}
	if string(data) != testData {
		return fmt.Errorf("read back %s expected %s", string(data), testData)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("error closing read file: %v", err)
	}
	return nil
}
