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
	"compress/zlib"
	"io"
)

type compressMode bool

const (
	compressed   compressMode = true
	uncompressed compressMode = false
)

func readCloser(r io.Reader, m compressMode) (io.ReadCloser, error) {
	if m == uncompressed {
		return &readerNOPCloser{r}, nil
	}

	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, err
	}

	return zr, nil
}

// The returned writeCloser must be closed before 'w' is closed to ensure
// internal buffering is flushed.
func writeCloser(w io.Writer, mode compressMode) io.WriteCloser {
	if mode == uncompressed {
		return &writerNOPCloser{w}
	}

	return zlib.NewWriter(w)
}

type readerNOPCloser struct {
	r io.Reader
}

func (r *readerNOPCloser) Read(buf []byte) (int, error) {
	return r.r.Read(buf)
}

func (r *readerNOPCloser) Close() error {
	return nil
}

type writerNOPCloser struct {
	w io.Writer
}

func (w *writerNOPCloser) Write(buf []byte) (int, error) {
	return w.w.Write(buf)
}

func (w *writerNOPCloser) Close() error {
	return nil
}
