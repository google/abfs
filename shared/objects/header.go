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

package objects

import (
	"bytes"
	"fmt"
	"io"
	"strconv"

	"github.com/google/abfs/shared/entities"
)

const MaxHeaderSize = 64

// PutHeader expects to receive an object type with the content of the object prefixed with a fixed
// header of MaxHeaderSize bytes. It will put the correct git header right before the actual content.
// It will return a slice where the actual content begins and includes the actual content.
func PutHeader(objType string, content []byte) ([]byte, error) {
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

	if err := WriteHeader(buf, objType, int64(bodyLen)); err != nil {
		return nil, err
	}
	if buf.Len() != hdrLen {
		return nil, fmt.Errorf("expected header size to be %d but got %d", hdrLen, buf.Len())
	}
	return content, nil
}

func WriteHeader(w io.Writer, objType string, size int64) error {
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

func ReadHeader(r io.Reader) (string, int64, error) {
	buf := make([]byte, MaxHeaderSize)
	i := 0
	for ; i < MaxHeaderSize; i++ {
		n, err := r.Read(buf[i : i+1])
		if err != nil && n != 1 {
			return "", 0, err
		}
		if n == 0 {
			return "", 0, io.ErrUnexpectedEOF
		}
		if buf[i] == 0 {
			break
		}
	}
	if i == MaxHeaderSize {
		return "", 0, entities.ErrObjectParse
	}

	return ParseHeader(buf[:i])
}

func ParseHeader(buf []byte) (string, int64, error) {
	p := bytes.IndexByte(buf, ' ')
	if p < 0 {
		return "", 0, entities.ErrObjectParse
	}
	t := string(buf[0:p])
	buf = buf[p+1:]
	s, err := strconv.ParseInt(string(buf), 10, 64)
	if err != nil {
		return "", 0, entities.ErrObjectParse
	}

	return t, s, nil
}

func ReadBlobHeader(r io.Reader) (int64, error) {
	t, size, err := ReadHeader(r)
	if err != nil {
		return 0, err
	}
	if t != string(entities.ObjectTypeBlob) {
		return 0, fmt.Errorf("expected blob type, got %s", t)
	}
	return size, nil
}

func ReadBlob(r io.Reader) ([]byte, error) {
	n, err := ReadBlobHeader(r)
	if err != nil {
		return nil, err
	}
	blob, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if n != int64(len(blob)) {
		return nil, fmt.Errorf("expected blob size %d, got %d", n, len(blob))
	}
	return blob, nil
}

func PrependHeaderReader(r io.Reader, objType string, size int64) io.Reader {
	header := bytes.NewBufferString(fmt.Sprintf("%s %d\x00", objType, size))

	return io.MultiReader(header, r)
}
