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
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/abfs/cas/hash"
)

const (
	Sha1Len    = 20
	Sha1HexLen = 40
)

type Sha1Hash [Sha1Len]byte

func (h *Sha1Hash) String() string {
	return hex.EncodeToString(h[:])
}
func (h *Sha1Hash) Slice() []byte {
	return h[:]
}
func (h *Sha1Hash) IsEmptyDir() bool {
	return *h == emptyDirHash
}
func (h *Sha1Hash) IsEmptyFile() bool {
	return *h == emptyFileHash
}
func (h *Sha1Hash) IsZero() bool {
	return *h == Sha1Hash{}
}
func (h *Sha1Hash) Equals(rhs hash.Hash) bool {
	return *h == *rhs.(*Sha1Hash)
}
func (h *Sha1Hash) Write(w io.Writer) (int, error) {
	return w.Write(h[:])
}
func (h *Sha1Hash) Read(r io.Reader) (int, error) {
	return io.ReadFull(r, h[:])
}
func (h *Sha1Hash) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.String())
}
func (h *Sha1Hash) Data() []byte {
	return h[:]
}
func (h *Sha1Hash) Clone() hash.Hash {
	var out Sha1Hash
	copy(out[:], h[:])

	return &out

}
func NewHash(s string) (*Sha1Hash, error) {
	if len(s) != Sha1HexLen {
		return nil, fmt.Errorf("invalid hash string len %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var hash Sha1Hash
	copy(hash[:], b)

	return &hash, nil
}
func IsValidHashString(val string) bool {
	if len(val) != Sha1HexLen {
		return false
	}
	_, err := hex.DecodeString(val)
	if err != nil {
		return false
	}
	return true
}
