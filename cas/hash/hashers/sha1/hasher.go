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

package sha1

import (
	"encoding/hex"

	"github.com/google/abfs/cas/hash"
)

var emptyFileHash Sha1Hash
var emptyDirHash Sha1Hash

func init() {
	_, _ = hex.Decode(emptyFileHash[:], []byte("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"))
	_, _ = hex.Decode(emptyDirHash[:], []byte("4b825dc642cb6eb9a060e54bf8d69288fbee4904"))
}

type hasher struct{}

var _ hash.Hasher = (*hasher)(nil)

func NewHasher() hash.Hasher {
	return &hasher{}
}

func (hasher) ZeroHash() hash.Hash {
	out := Sha1Hash{}
	return &out
}
func (hasher) EmptyFileHash() hash.Hash {
	out := emptyFileHash
	return &out
}
func (hasher) EmptyDirHash() hash.Hash {
	out := emptyDirHash
	return &out
}
func (hasher) NewHash(hashStr string) (hash.Hash, error) {
	return NewHash(hashStr)
}
func (hasher) IsValidHashString(val string) bool {
	return IsValidHashString(val)
}
func (hasher) New() hash.HashFunc {
	return NewHashFunc()
}
