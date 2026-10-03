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

package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/go-git/go-git/plumbing"
	"github.com/google/abfs/casfuse"
)

// Can be a git object or git blob
// Both are handled by this blob object
type blob struct {
	file   *os.File
	reader ReaderAtSize
	info   *Info
	size   int64 //hold size seperately since we need to modify for blobs
}

func newBlob(info *Info) *blob {
	return &blob{info: info}
}
func (b *blob) doOpen() error {
	f, err := os.Open(b.info.FilePath)
	if err != nil {
		return err
	}
	size := b.info.FileInfo.Size()
	b.file = f

	if b.info.GitType == GitTypeBlob {
		header := bytes.NewBuffer(make([]byte, 0, 20))
		fmt.Fprintf(header, "%s %d\x00", plumbing.BlobObject, size)

		r := NewMultiSectionReader(
			io.NewSectionReader(bytes.NewReader(header.Bytes()), 0, int64(header.Len())),
			io.NewSectionReader(b.file, 0, size),
		)

		// Update our size
		size = r.Size()
		b.reader = NewNoCompressReader(r, uint64(size))
		size = b.reader.Size()

	} else {
		b.reader = io.NewSectionReader(b.file, 0, size)
		size = b.reader.Size()
	}
	b.size = size
	return nil
}
func (b *blob) Open() error {
	return nil
}
func (b *blob) Close() error {
	b.reader = nil
	return b.file.Close()
}
func (b *blob) Truncate() error {
	return casfuse.ErrNotSupported
}
func (b *blob) Flush() error {
	return nil
}
func (b *blob) Fsync() error {
	return nil
}
func (b *blob) Fallocate(int, int64, int64) error {
	return casfuse.ErrNotSupported
}
func (b *blob) SetSize(int64) error {
	return casfuse.ErrNotSupported
}
func (b *blob) Size() int64 {
	return b.size
}
func (b *blob) Lseek(offset int64, whence uint32) (int64, error) {
	return 0, casfuse.ErrNotSupported
}
func (b *blob) Poll() (bool, bool) {
	return true, true
}
func (b *blob) ReadAt(p []byte, off int64) (int, error) {
	n, err := b.reader.ReadAt(p, off)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return n, err
}
func (b *blob) WriteAt(p []byte, off int64) (int, error) {
	return 0, casfuse.ErrNotSupported
}
