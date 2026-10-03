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

package ctrltap

import (
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs/fscas/tap"
)

type blob struct {
	session  *tap.Session
	sessions tap.Sessions
	offset   int64
}

func (b *blob) Open() error {
	return nil
}
func (b *blob) Close() error {
	b.sessions.DeleteSession(b.session)
	b.session = nil
	return nil
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
	out := b.session.Size()
	return out
}
func (b *blob) Lseek(offset int64, whence uint32) (int64, error) {
	return 0, nil
}
func (b *blob) Poll() (bool, bool) {
	ret := b.session.Size() > b.offset
	return ret, false
}
func (b *blob) ReadAt(p []byte, off int64) (int, error) {
	n, err := b.session.Read(p)
	b.offset = off + int64(n)

	return n, err
}
func (b *blob) WriteAt(p []byte, off int64) (int, error) {
	return 0, casfuse.ErrNotSupported
}
