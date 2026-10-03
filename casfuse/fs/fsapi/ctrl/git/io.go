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
	"io"
)

type ReaderAtSize interface {
	io.ReaderAt
	Size() int64
}

type MultiSectionReader struct {
	readers []ReaderAtSize
	sizes   []int64
}

func (m *MultiSectionReader) Size() int64 {
	return m.sizes[len(m.sizes)-1]
}

func (m *MultiSectionReader) ReadAt(buf []byte, off int64) (n int, err error) {
	p := 0
	remain := len(buf)

	for i := m.index(off); i < len(m.readers) && remain > 0; i++ {
		if off != 0 && i > 0 {
			off -= m.sizes[i-1]
		}

		toRead := remain
		if toRead > int(m.readers[i].Size()) {
			toRead = int(m.readers[i].Size())
		}

		n, err := ReadFullAt(m.readers[i], buf[p:p+toRead], off)
		if err != nil && err != io.EOF {
			return p, err
		}
		remain -= n
		p += n
		off = 0
	}
	if p < len(buf) {
		err = io.EOF
	} else {
		err = nil
	}
	return p, err
}

func (m *MultiSectionReader) index(offset int64) int {
	sizes := m.sizes
	start := 0
	end := len(sizes)
	for start < end {
		mid := (start + end) / 2
		if sizes[mid] <= offset {
			start = mid + 1
		} else {
			end = mid
		}
	}
	return start
}

func NewMultiSectionReader(readers ...ReaderAtSize) *MultiSectionReader {
	sizes := make([]int64, len(readers))
	var s int64
	for i, r := range readers {
		s += r.Size()
		sizes[i] = s
	}
	return &MultiSectionReader{readers: readers, sizes: sizes}
}

func ReadFullAt(r io.ReaderAt, buf []byte, off int64) (n int, err error) {
	for n < len(buf) && err == nil {
		var p int
		p, err = r.ReadAt(buf[n:], off+int64(n))
		n += p
	}
	return n, err
}
