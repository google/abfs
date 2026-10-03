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

package mem

import "github.com/google/abfs/casfuse"

type Blob struct {
	data []byte
}

func (b *Blob) Data() []byte {
	return b.data
}
func (b *Blob) Open() error {
	return nil
}
func (b *Blob) Close() error {
	return nil

}
func (b *Blob) Size() int64 {
	return int64(len(b.data))
}
func (b *Blob) SetSize(size int64) error {
	b.data = b.data[:size]
	return nil

}
func (b *Blob) Truncate() error {
	return b.SetSize(0)
}

func (b *Blob) Fallocate(mode int, offset, len int64) error {
	// Add support
	return nil
}
func (b *Blob) Flush() error {
	return nil
}
func (b *Blob) Fsync() error {
	return nil
}
func (b *Blob) Lseek(offset int64, whence uint32) (int64, error) {
	return 0, casfuse.ErrNotSupported
}
func (b *Blob) Poll() (bool, bool) {
	return true, true
}
func (b *Blob) ReadAt(p []byte, off int64) (n int, err error) {
	if off < 0 || off >= int64(len(b.data)) {
		return 0, nil
	}
	n = copy(p, b.data[off:])
	return n, nil
}
func (b *Blob) WriteAt(p []byte, off int64) (int, error) {
	totalLen := len(p) + int(off)
	if totalLen > cap(b.data) {
		nd := make([]byte, totalLen)
		copy(nd, b.data)
		b.data = nd
	}
	if int(off) > len(b.data) {
		b.data = b.data[:int(off)]
	}
	copy(b.data[off:], p)
	if totalLen > len(b.data) {
		b.data = b.data[:totalLen]
	}
	return len(p), nil
}
