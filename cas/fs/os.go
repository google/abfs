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
	"os"
	"syscall"

	"github.com/google/abfs/cas/cas"
)

// Provide FSAPI via OS library calls
type OSFSAPI struct {
}

func NewOSFSAPI() *OSFSAPI {
	return &OSFSAPI{}
}

var _ FSAPI = (*OSFSAPI)(nil)

func (OSFSAPI) Getwd() (string, error) {
	return os.Getwd()
}
func (OSFSAPI) Chdir(path string) error {
	return os.Chdir(path)
}
func (OSFSAPI) Mkdir(path string, perm cas.FileMode) error {
	return os.Mkdir(path, perm)
}
func (OSFSAPI) MkdirAll(path string, perm cas.FileMode) error {
	return os.MkdirAll(path, perm)
}
func (OSFSAPI) Remove(path string) error {
	return os.Remove(path)
}
func (OSFSAPI) Rename(oldName, newName string) error {
	return os.Rename(oldName, newName)
}
func (OSFSAPI) Symlink(oldName, newName string) error {
	return os.Symlink(oldName, newName)
}
func (OSFSAPI) Readlink(name string) (string, error) {
	return os.Readlink(name)
}
func (OSFSAPI) Link(oldName, newName string) error {
	return os.Link(oldName, newName)
}
func (OSFSAPI) Mknod(path string, mode cas.FileMode, rDev int) error {
	return syscall.Mknod(path, uint32(FSModeToOSMode(mode)), rDev)
}
func (OSFSAPI) Lstat(path string) (cas.FileInfo, error) {
	return os.Lstat(path)
}
func (OSFSAPI) Stat(path string) (cas.FileInfo, error) {
	return os.Stat(path)
}
func (OSFSAPI) Chmod(path string, mode cas.FileMode) error {
	return os.Chmod(path, mode)
}
func (OSFSAPI) OpenFile(path string, flag int, perm cas.FileMode) (FSFile, error) {
	return os.OpenFile(path, flag, perm)
}
