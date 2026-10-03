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

package pt

import (
	"fmt"
	"math/rand"
	"os"
	"sync"
	"syscall"
	"time"
)

type blob struct {
	fd      int
	path    string
	size    int64
	baseDir string
}

func allocPath(baseDir string) (string, int, error) {

	var name string
	for {
		rnd := nextRandom()
		name = fmt.Sprintf("%s/%d", baseDir, rnd)
		fd, err := syscall.Open(name, syscall.O_CREAT|syscall.O_RDWR|syscall.O_EXCL, 0600)
		if !os.IsExist(err) {
			if err != nil {
				return name, -1, err
			}
			return name, fd, nil
		}
	}
}
func (b *blob) updateWriteSize(size int64) {
	if b.size < size {
		b.size = size
	}
}
func (b *blob) Open() error {
	if b.fd == -1 {
		if b.path == "" {
			path, fd, err := allocPath(b.baseDir)
			if err != nil {
				return err
			}
			b.fd = fd
			b.path = path
		} else {
			fd, err := syscall.Open(b.path, syscall.O_APPEND|syscall.O_RDWR|syscall.O_EXCL, 0600)
			if err != nil {
				return err
			}
			b.fd = fd
		}
	}
	return nil
}
func (b *blob) Close() error {
	if b.fd != 1 {
		if err := syscall.Close(b.fd); err != nil {
			return err
		}
	}
	b.fd = -1
	return nil
}
func (b *blob) Size() int64 {
	return int64(b.size)
}
func (b *blob) SetSize(size int64) error {
	if err := syscall.Ftruncate(b.fd, int64(size)); err != nil {
		return err
	}
	b.size = size
	return nil

}
func (b *blob) Truncate() error {
	return b.SetSize(0)
}

func (b *blob) Fallocate(mode int, offset, length int64) error {
	if err := syscall.Fallocate(b.fd, 0, offset, length); err != nil {
		return err
	}
	b.updateWriteSize(int64(offset + length))
	return nil

}
func (b *blob) Flush() error {
	return syscall.Fsync(b.fd)
}
func (b *blob) Fsync() error {
	return syscall.Fsync(b.fd)
}
func (b *blob) ReadAt(p []byte, off int64) (n int, err error) {
	return syscall.Pread(b.fd, p, off)
}
func (b *blob) Lseek(offset int64, whence uint32) (int64, error) {
	return syscall.Seek(b.fd, offset, int(whence))
}
func (b *blob) Poll() (bool, bool) {
	return true, true
}
func (b *blob) WriteAt(p []byte, off int64) (int, error) {
	n, err := syscall.Pwrite(b.fd, p, off)
	if err != nil {
		return 0, err
	}
	b.updateWriteSize(int64(off + int64(n)))
	return n, nil
}

var randomLock sync.Mutex
var random = rand.New(rand.NewSource(time.Now().Unix()))

func nextRandom() int {
	randomLock.Lock()
	defer randomLock.Unlock()
	return random.Int()
}
