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

package entities

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
)

type ObjectType string

const (
	ObjectTypeBlob    ObjectType = "blob"
	ObjectTypeUnknown ObjectType = "unknown"
)

type ObjectHeader struct {
	Type           ObjectType
	Size           int64
	CompressedSize int64 // size after zlib compressed
}

func (h *ObjectHeader) IsEmpty() bool {
	return h.Type == ""
}

func (h *ObjectHeader) IsBlob() bool {
	return h.Type == ObjectTypeBlob
}

func (h *ObjectHeader) FileSize() int64 {
	s := 2 + h.Size + int64(len(h.Type))
	if h.Size == 0 {
		return s + 1
	}
	n := h.Size
	for n > 0 {
		n /= 10
		s++
	}
	return s
}

// Note: This only handle the case of compression level 0
func BlobSizeAfterCompressed(fileSize int64) int64 {
	zlibBlock := int64(65535)
	headerSize := int64(6 + len(strconv.FormatInt(fileSize, 10)))
	totalDataSize := fileSize + headerSize
	numBlocks := (totalDataSize + zlibBlock - 1) / zlibBlock
	zlibOverhead := int64(6 + (numBlocks * 5))
	finalSize := totalDataSize + zlibOverhead
	return finalSize
}

func DecodeObject(content []byte) (string, []byte, error) {
	p := bytes.IndexByte(content, ' ')
	if p < 0 {
		return "", nil, ErrObjectParse
	}
	t := string(content[0:p])
	content = content[p+1:]
	p = bytes.IndexByte(content, 0)
	if p < 0 {
		return "", nil, ErrObjectParse
	}
	s, err := strconv.ParseInt(string(content[0:p]), 10, 64)
	if err != nil {
		return "", nil, ErrObjectParse
	}
	content = content[p+1:]
	if s != int64(len(content)) {
		return "", nil, ErrObjectParse
	}

	return t, content, nil
}

func WriteObject(objType string, content []byte, w io.Writer) error {
	_, err := fmt.Fprintf(w, "%s %d\x00", objType, len(content))
	if err != nil {
		return err
	}
	_, err = w.Write(content)
	return err
}
