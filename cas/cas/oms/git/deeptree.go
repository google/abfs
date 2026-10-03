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
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
)

type DeepTreeReader struct {
	f      *openFile
	hasher hash.Hasher
}

func NewDeepTreeReader(f *openFile, h hash.Hasher) *DeepTreeReader {
	return &DeepTreeReader{f: f, hasher: h}
}
func (d *DeepTreeReader) Close() error {
	d.f.Close()
	return nil
}
func (d *DeepTreeReader) Read() (*cas.DirEntry, error) {
	var raw []byte
	var err error

	// Read the hash
	hash := d.hasher.ZeroHash()
	_, err = hash.Read(d.f.Reader())
	if err != nil {
		return nil, err
	}

	sz, err := readVarint(d.f.Reader())
	if err != nil {
		return nil, err
	}
	mode, err := readVarint(d.f.Reader())
	if err != nil {
		return nil, err
	}
	osMode, extType, err := GetTypeFromMode(Mode(mode))
	if err != nil {
		return nil, err
	}
	var delim byte = 0
	raw, err = readDelim(d.f.Reader(), &delim, false)
	if err != nil {
		return nil, err
	}
	name := string(raw)

	out := &cas.DirEntry{
		Hash:    hash,
		Size:    int64(sz),
		Mode:    osMode,
		Name:    name,
		ExtType: extType}

	return out, nil
}
