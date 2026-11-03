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

package depot

import (
	"io"
	"sync"
)

var copyBufferPool = sync.Pool{
	New: func() any {
		return make([]byte, 32*1024)
	},
}

func copyNBuffer(dst io.Writer, src io.Reader, n int64) (written int64, err error) {
	buf := copyBufferPool.Get().([]byte)
	defer copyBufferPool.Put(buf)

	written, err = io.CopyBuffer(dst, io.LimitReader(src, n), buf)
	if written == n {
		return n, nil
	}
	if written < n && err == nil {
		err = io.EOF
	}
	return written, err
}

var inlineStorePool = sync.Pool{
	New: func() any {
		return make([]byte, 32*1024)
	},
}

var chunkStorePool = sync.Pool{
	New: func() any {
		return make([]byte, 1024*1024)
	},
}

type sliceWriter struct {
	buf []byte
	off int
}

func (w *sliceWriter) Write(p []byte) (n int, err error) {
	if w.off >= len(w.buf) {
		return 0, io.ErrShortWrite
	}
	n = copy(w.buf[w.off:], p)
	w.off += n
	if n < len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func (w *sliceWriter) Bytes() []byte {
	return w.buf[:w.off]
}

type pooledBufferReader struct {
	buf  []byte
	off  int
	pool *sync.Pool
}

func (r *pooledBufferReader) Read(p []byte) (n int, err error) {
	if r.off >= len(r.buf) {
		if r.buf != nil {
			r.pool.Put(r.buf[:cap(r.buf)])
			r.buf = nil
		}
		return 0, io.EOF
	}
	n = copy(p, r.buf[r.off:])
	r.off += n
	if r.off >= len(r.buf) {
		if r.buf != nil {
			r.pool.Put(r.buf[:cap(r.buf)])
			r.buf = nil
		}
		err = io.EOF
	}
	return n, err
}
