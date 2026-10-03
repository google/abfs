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
	"crypto/sha1"
	"hash"

	cashash "github.com/google/abfs/cas/hash"
)

type hashFunc struct {
	hasher hash.Hash
}

var _ cashash.HashFunc = (*hashFunc)(nil)

func NewHashFunc() *hashFunc {
	return &hashFunc{hasher: sha1.New()}
}
func (h *hashFunc) Sum(d []byte) cashash.Hash {
	out := Sha1Hash{}
	h.hasher.Write(d)
	copy(out[:], h.hasher.Sum(nil))
	return &out
}
func (h *hashFunc) Write(p []byte) (int, error) {
	return h.hasher.Write(p)
}

func (h *hashFunc) Size() int {
	return h.hasher.Size()
}
