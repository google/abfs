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
	"errors"
	"fmt"
)

var ErrObjectParse = errors.New("failed to parse the object")

type FileEntry struct {
	Name string
	Mode FileMode
	Hash Hash
	Size int64
}

func DecodeDir(buf []byte, meta TreeMeta) ([]FileEntry, error) {
	entries, err := DecodeDirPlain(buf)
	if err != nil {
		return nil, err
	}
	if len(entries) != len(meta.Sizes) {
		return nil, fmt.Errorf("dir entries count (%d) is different from sizes (%d): %w", len(entries), len(meta.Sizes), ErrObjectParse)
	}
	for i := range entries {
		entries[i].Size = meta.Sizes[i]
	}
	return entries, nil
}

// DecodeDirPlain is similar to DecodeDir, but the sizes of the entries won't be present in the
// returned objects since they weren't provided.
func DecodeDirPlain(buf []byte) ([]FileEntry, error) {
	t, buf, err := DecodeObject(buf)
	if err != nil {
		return nil, err
	}
	if ObjectType(t) != "tree" {
		return nil, fmt.Errorf("object is not a tree (is a %s): %w", t, ErrObjectParse)
	}

	var entries []FileEntry
	var i int
	for i = 0; ; i++ {
		p := bytes.IndexByte(buf, ' ')
		if p < 0 {
			break
		}
		mode, err := FileModeNew(string(buf[:p]))
		if err != nil {
			return nil, err
		}
		buf = buf[p+1:]
		if p = bytes.IndexByte(buf, 0); p < 0 {
			return nil, ErrObjectParse
		}

		name := string(buf[0:p])

		var hash Hash
		copy(hash[:], buf[p+1:])

		buf = buf[p+21:]

		entries = append(entries, FileEntry{
			Name: name,
			Mode: FileMode(mode),
			Hash: hash,
		})
	}
	return entries, nil
}
