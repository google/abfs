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

package fscas

import (
	"os"
	"syscall"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse"
)

type openMode int

const (
	openModeClosed openMode = iota
	openModeLoaded
	openModeRo
	openModeRw
)

type blob struct {
	openMode openMode
	fd       int
	path     string
	file     *file
}

func (b *blob) fileSize() int64 {
	if b.file != nil {
		return b.file.Size()
	}
	return 0

}
func (b *blob) setFileSize(size int64) {
	b.file.updateInodeFromBlob(size)
}
func (b *blob) updateWriteSize(size int64) {
	if b.fileSize() < size {
		b.file.updateInodeFromBlob(size)
	}
}

func (b *blob) casStore() cas.Store {
	return GetStore(b.file.FS())
}
func (b *blob) storeWriter() StoreWriter {
	return GetStoreWriter(b.file.FS())
}
func (b *blob) isDirty() bool {
	return b.file.IsDirty()
}
func (b *blob) hash() hash.Hash {
	return b.file.hash
}
func (b *blob) clearDirty() {
	b.file.ClearDirty()
}
func (b *blob) setDirty() {
	b.file.markDirty()
}
func (b *blob) Size() int64 {
	return b.fileSize()
}

func (b *blob) Init() {
	b.openMode = openModeClosed
	b.fd = -1
	b.path = ""
	b.clearDirty()
}

// Open blob file, internally this corresponds to loading
// the file.  The file is not actually opened until it
// is accessed
func (b *blob) Open() error {
	switch b.openMode {
	case openModeLoaded:
		return nil
	case openModeClosed:
		return b.load()
	}

	return casfuse.ErrInvalidState
}

// Close the blob file
func (b *blob) Close() error {
	if b.fd != -1 {
		if err := syscall.Close(b.fd); err != nil {
			return err
		}
		b.fd = -1
	}

	if b.isDirty() {
		b.openMode = openModeLoaded
	} else {
		b.openMode = openModeClosed
	}
	return nil
}

func (b *blob) Truncate() error {
	b.SetSize(0)
	return nil
}

// Reset any blob back to closed state
func (b *blob) Reset() {
	_ = b.Close()
	if b.isDirty() {
		b.storeWriter().RemoveStagingFile(b.path)
	}
	b.Init()

}

func (b *blob) load() error {
	if b.openMode != openModeClosed {
		return casfuse.ErrInvalidState
	}
	path, _, err := b.storeWriter().FilePath(b.hash())
	if err != nil {
		// Allow for an empty file to be created here it will fail
		// later in cas_blob_open_ro() but will be successful in
		// cas_blob_open_rw()
		if !b.hash().IsEmptyFile() {
			return err
		}
		err = nil
	} else {
		b.path = path
	}

	b.openMode = openModeLoaded
	return nil
}
func (b *blob) openRO() error {

	if b.openMode != openModeLoaded {
		return casfuse.ErrInvalidState
	}
	// Open our path for read only
	fd, err := syscall.Open(b.path, os.O_RDONLY, 0644)
	if err != nil {
		return err
	}
	b.fd = fd
	b.openMode = openModeRo
	return nil
}

func (b *blob) openRW() error {

	if (b.openMode != openModeLoaded) && (b.openMode != openModeRo) {
		return casfuse.ErrInvalidState
	}

	// If file is dirty it is still available at our path and does not
	// need a new temp file
	if b.isDirty() {
		fd, err := syscall.Open(b.path, syscall.O_RDWR|syscall.O_EXCL, 0600)
		if err != nil {
			return err
		}
		b.fd = fd
		b.openMode = openModeRw
		return nil
	}

	if b.openMode == openModeLoaded {
		// Can have entry in which case the file needs to be copied
		// or can have none which is just an empty file.
		if len(b.path) != 0 {
			err := b.openRO()
			if err != nil {
				return err
			}
		} else {
			// mark open mode for empty file
			b.openMode = openModeRo
		}
	}
	// Create a new temporary file
	path, fd, err := b.storeWriter().CreateStagingFile()
	if err != nil {
		return err
	}
	// If we have a blob file copy the data otherwise
	// starting from an emtpy file
	if b.fd != -1 {
		err = copyFD(b.fd, fd)
		syscall.Close(b.fd)
	}
	b.fd = fd
	b.path = path
	b.setDirty()
	b.openMode = openModeRw

	return nil
}

func (b *blob) ensureRead() error {
	switch b.openMode {
	case openModeRo, openModeRw:
		return nil
	}
	return b.openRO()
}
func (b *blob) ensureWrite() error {
	if b.openMode == openModeRw {
		return nil
	}
	return b.openRW()
}
func (b *blob) Flush() error {
	if b.openMode == openModeRw {
		return syscall.Fsync(b.fd)
	}
	return nil
}
func (b *blob) Fsync() error {
	if b.openMode == openModeRw {
		return syscall.Fsync(b.fd)
	}
	return nil
}

func (b *blob) ReadAt(p []byte, off int64) (int, error) {

	if err := b.ensureRead(); err != nil {
		return 0, err
	}
	n, err := syscall.Pread(b.fd, p, off)
	return n, err
}

func (b *blob) WriteAt(p []byte, off int64) (int, error) {
	if err := b.ensureWrite(); err != nil {
		return 0, err
	}

	n, err := syscall.Pwrite(b.fd, p, off)
	if err != nil {
		return 0, err
	}
	s := off + int64(n)
	b.updateWriteSize(s)
	return n, nil
}

func (b *blob) Fallocate(mode int, offset, length int64) (err error) {
	if err := b.ensureWrite(); err != nil {
		return err
	}

	if err := syscall.Fallocate(b.fd, 0, offset, length); err != nil {
		return err
	}

	s := offset + length
	b.updateWriteSize(s)
	return nil
}

func (b *blob) SetSize(n int64) error {
	// If no change to the file size prevent loading the file
	if n == b.fileSize() {
		return nil
	}
	if err := b.ensureWrite(); err != nil {
		return err
	}
	if err := syscall.Ftruncate(b.fd, int64(n)); err != nil {
		return err
	}
	b.setFileSize(n)
	return nil
}

func (b *blob) Snapshot() (hash.Hash, error) {
	if !b.isDirty() {
		return nil, casfuse.ErrNotDirty
	}

	if err := b.Close(); err != nil {
		return nil, err
	}

	newHash, err := b.casStore().SaveFile(b.path, b.fileSize())
	if err != nil {
		return nil, err
	}
	b.clearDirty()
	b.Reset()
	return newHash, nil

}
func (b *blob) Lseek(offset int64, whence uint32) (int64, error) {
	return 0, casfuse.ErrNotSupported
}
func (b *blob) Poll() (bool, bool) {
	return true, true
}

// copyFD() is similar to io.Copy() but uses file-descriptors instead of readers/writers.
func copyFD(srcFD int, dstFD int) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := syscall.Read(srcFD, buf[:])
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		for i := 0; i < n; {
			m, err := syscall.Write(dstFD, buf[i:n])
			if err != nil {
				return err
			}
			i += m
		}
	}
}
