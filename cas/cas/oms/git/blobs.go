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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
)

type Blobs struct {
	Store
}

func NewBlobs(path string, hloc HashToFileLoc, h hash.Hasher) *Blobs {
	return &Blobs{Store: newStore(path, hloc, h)}
}
func (b *Blobs) Save(r io.Reader) (hash.Hash, error) {
	path, size, err := b.writeScratchFile(r, uncompressed)
	if err != nil {
		return nil, err
	}
	return b.SaveFile(path, size)

}

func (b *Blobs) SaveFile(filepath string, size int64) (hash.Hash, error) {
	hash, err := HashFile(b.Hasher, filepath, size)
	if err != nil {
		return nil, err
	}

	loc, _, err := b.Path(hash)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	err = b.commitFile(filepath, loc)
	return hash, err
}

func (b *Blobs) SaveSymLink(target string) (hash.Hash, error) {
	h, err := HashSymLink(b.Hasher, target)
	err = b.Write(h, strings.NewReader(target), uncompressed)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (b *Blobs) OpenFile(h hash.Hash) (io.ReadCloser, error) {
	return b.Open(h, uncompressed)
}
func (b *Blobs) GetSymLink(h hash.Hash) (string, error) {
	var out string
	raw, err := b.ReadAll(h, uncompressed)
	if err != nil {
		return out, err
	}
	out = string(raw)
	return out, nil
}
func (b *Blobs) hashBlob(size int64, r io.Reader) (hash.Hash, error) {
	sHasher := b.Hasher.New()

	if err := writeHeader(sHasher, "blob", size); err != nil {
		return nil, err
	}

	if _, err := io.Copy(sHasher, r); err != nil {
		return nil, err
	}
	out := sHasher.Sum(nil)
	return out, nil

}

func (b *Blobs) Query(h hash.Hash) (*cas.QueryInfo, error) {
	_, fi, err := b.Path(h)
	if err == nil {
		return &cas.QueryInfo{Type: cas.StoreItemTypeData, Size: fi.Size()}, nil

	}
	return nil, fmt.Errorf("unknown hash")
}

func (b *Blobs) WriteBlob(h hash.Hash, reader io.Reader) error {
	return b.Write(h, reader, uncompressed)
}
