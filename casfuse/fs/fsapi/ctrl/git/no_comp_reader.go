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
	"encoding/binary"
	"io"
)

var (
	zlibMagic = []byte{0x78, 0x01}
	contBlock = []byte{0x00, 0xfb, 0xff, 0x04, 0x00}
)

const (
	modAdler      uint32 = 65521
	blockSize            = 65531
	blockConsumes        = blockSize + 5 /* len(contBlock) */
)

type NoCompressReader struct {
	*MultiSectionReader
}

func NewNoCompressReader(r io.ReaderAt, size uint64) *NoCompressReader {
	cc := int((size + blockSize - 1) / blockSize)
	readers := make([]ReaderAtSize, 0, 2+2*cc)
	readers = append(readers, io.NewSectionReader(bytes.NewReader(zlibMagic), 0, int64(len(zlibMagic))))
	s := blockSize
	for i := 0; i < cc; i++ {
		if i < cc-1 {
			readers = append(readers, io.NewSectionReader(bytes.NewReader(contBlock), 0, int64(len(contBlock))))
		} else {
			s = int((size-1)%blockSize + 1)
			block := make([]byte, 0, len(contBlock))
			block = append(block, 0x01)
			block = binary.LittleEndian.AppendUint16(block, uint16(s))
			block = binary.LittleEndian.AppendUint16(block, ^uint16(s))
			readers = append(readers, io.NewSectionReader(bytes.NewReader(block), 0, int64(len(block))))
		}
		readers = append(readers, io.NewSectionReader(r, int64(i*blockSize), int64(s)))
	}
	readers = append(readers, io.NewSectionReader(AdlerReader(r, size), 0, 4))
	return &NoCompressReader{NewMultiSectionReader(readers...)}
}

type adlerReader struct {
	r    io.ReaderAt
	size uint64
}

func AdlerReader(r io.ReaderAt, size uint64) *adlerReader {
	return &adlerReader{r, size}
}

func (r *adlerReader) ReadAt(p []byte, offset int64) (n int, err error) {
	var a, b uint32 = 1, 0

	buf := make([]byte, blockConsumes)
	read := uint64(0)
	for read < r.size {
		buf = buf[:]
		m, err := r.r.ReadAt(buf, int64(read))
		if err != nil && err != io.EOF {
			return 0, err
		}
		if m == 0 {
			break
		}
		read += uint64(m)
		for _, byte := range buf[:m] {
			a = (a + uint32(byte)) % modAdler
			b = (b + a) % modAdler
		}
	}

	buf = binary.BigEndian.AppendUint32(nil, (b<<16)|a)
	return copy(p, buf[offset:]), nil
}
