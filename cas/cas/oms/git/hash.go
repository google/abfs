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
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/google/abfs/cas/hash"
)

const (
	MaxHeaderSize = 64
)

// PutHeader expects to receive an object type with the content of the object prefixed with a fixed
// header of MaxHeaderSize bytes. It will put the correct git header right before the actual content.
// It will return a slice where the actual content begins and includes the actual content.
func putHeader(objType string, content []byte) ([]byte, error) {
	bodyLen := len(content) - MaxHeaderSize
	if bodyLen < 0 {
		return nil, fmt.Errorf("content is not long enough (is %d) to contain the header padding (expected at least %d)", len(content), MaxHeaderSize)
	}
	s := 0
	for n := bodyLen; n > 0; n /= 10 {
		s++
	}
	if bodyLen == 0 {
		s = 1
	}

	hdrLen := s + 2 + len(objType)
	if hdrLen > MaxHeaderSize {
		return nil, fmt.Errorf("object header %q is longer than expected", objType)
	}

	content = content[MaxHeaderSize-hdrLen:]
	buf := bytes.NewBuffer(content[:0])

	if err := writeHeader(buf, objType, int64(bodyLen)); err != nil {
		return nil, err
	}
	if buf.Len() != hdrLen {
		return nil, fmt.Errorf("expected header size to be %d but got %d", hdrLen, buf.Len())
	}
	return content, nil
}

func writeHeader(w io.Writer, objType string, size int64) error {
	if _, err := w.Write([]byte(objType)); err != nil {
		return err
	}
	if _, err := w.Write([]byte(" ")); err != nil {
		return err
	}
	if _, err := w.Write([]byte(strconv.FormatInt(size, 10))); err != nil {
		return err
	}
	if _, err := w.Write([]byte{0}); err != nil {
		return err
	}
	return nil
}

func HashFile(hasher hash.Hasher, filepath string, size int64) (hash.Hash, error) {
	f, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return hashBlob(hasher, size, f)
}
func HashSymLink(hasher hash.Hasher, target string) (hash.Hash, error) {
	return hashBlob(hasher, int64(len(target)), strings.NewReader(target))

}
func hashBlob(hasher hash.Hasher, size int64, r io.Reader) (hash.Hash, error) {
	sHasher := hasher.New()

	if err := writeHeader(sHasher, "blob", size); err != nil {
		return nil, err
	}

	if _, err := io.Copy(sHasher, r); err != nil {
		return nil, err
	}
	out := sHasher.Sum(nil)
	return out, nil

}
