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
	"encoding"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/abfs/third_party/go-git/plumbing"
	"github.com/google/abfs/third_party/go-git/plumbing/hash"
)

// The basic hash helpers below delegate to the go-git code in
// third_party/go-git.

const (
	// HashSize is the number of bytes in a Hash.
	HashSize = hash.Size
	// HashHexSize is the length of a Hash in hexadecimal notation.
	HashHexSize = hash.HexSize
)

// Hash is a SHA-1 object hash.
type Hash [HashSize]byte

var _ json.Marshaler = (*Hash)(nil)
var _ json.Unmarshaler = (*Hash)(nil)

// encoding.TextMarshaler and encoding.TextUnmarshaler are required to use Hash as a key in JSON maps
var _ encoding.TextMarshaler = (*Hash)(nil)
var _ encoding.TextUnmarshaler = (*Hash)(nil)

var (
	ZeroHash      = Hash(plumbing.ZeroHash)
	EmptyFileHash Hash
	EmptyDirHash  Hash
)

func init() {
	_, _ = hex.Decode(EmptyFileHash[:], []byte("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"))
	_, _ = hex.Decode(EmptyDirHash[:], []byte("4b825dc642cb6eb9a060e54bf8d69288fbee4904"))
}

var EmptyFileContent = []byte("blob 0\x00")
var EmptyDirContent = []byte("tree 0\x00")

// String returns h in hexadecimal notation.
func (h Hash) String() string {
	return plumbing.Hash(h).String()
}

// IsZero reports whether h is the zero hash.
func (h Hash) IsZero() bool {
	return plumbing.Hash(h).IsZero()
}

// NewHash parses a hexadecimal hash. Invalid input yields a zero or partial
// hash; use IsHash to validate first.
func NewHash(s string) Hash {
	return Hash(plumbing.NewHash(s))
}

// IsHash reports whether s is a valid hexadecimal hash.
func IsHash(s string) bool {
	return plumbing.IsHash(s)
}

// MarshalJSON implements the json.Marshaler interface for Hash.
func (h Hash) MarshalJSON() ([]byte, error) {
	if h.IsZero() {
		return json.Marshal("")
	}
	return json.Marshal(h.String())
}

// UnmarshalJSON implements the json.Unmarshaler interface for Hash.
func (h *Hash) UnmarshalJSON(data []byte) error {
	var hex string
	if err := json.Unmarshal(data, &hex); err != nil {
		return err
	}
	if hex == "" {
		*h = ZeroHash
		return nil
	}
	if !IsHash(hex) {
		return fmt.Errorf("invalid hash %q", hex)
	}
	*h = NewHash(hex)
	return nil
}

// MarshalText implements encoding.TextMarshaler
func (h Hash) MarshalText() ([]byte, error) {
	if h.IsZero() {
		return []byte(""), nil
	}
	return []byte(h.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler
func (h *Hash) UnmarshalText(text []byte) error {
	hexStr := string(text)
	if hexStr == "" {
		*h = ZeroHash
		return nil
	}
	if !IsHash(hexStr) {
		return fmt.Errorf("invalid hash %q", hexStr)
	}
	*h = NewHash(hexStr)
	return nil
}

type SyncHashSet [256]*sync.Map

func NewHashSet() SyncHashSet {
	var s SyncHashSet
	for i := range s {
		s[i] = &sync.Map{}
	}
	return s
}

func (s SyncHashSet) Put(h Hash) bool {
	_, exists := s[h[0]].LoadOrStore(h, struct{}{})
	return !exists
}

func (s SyncHashSet) Has(h Hash) bool {
	_, exists := s[h[0]].Load(h)
	return exists
}
