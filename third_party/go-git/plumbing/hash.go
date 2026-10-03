// Copyright 2018 Sourced Technologies, S.L., Google LLC
// SPDX-License-Identifier: Apache-2.0
// See the LICENSE file in third_party/go-git for the full license text.

package plumbing

import (
	"encoding/hex"

	"github.com/google/abfs/third_party/go-git/plumbing/hash"
)

// Hash SHA1 hashed content
type Hash [hash.Size]byte

// ZeroHash is Hash with value zero
var ZeroHash Hash

// NewHash return a new Hash from a hexadecimal hash representation
func NewHash(s string) Hash {
	b, _ := hex.DecodeString(s)

	var h Hash
	copy(h[:], b)

	return h
}

func (h Hash) IsZero() bool {
	var empty Hash
	return h == empty
}

func (h Hash) String() string {
	return hex.EncodeToString(h[:])
}

// IsHash returns true if the given string is a valid hash.
func IsHash(s string) bool {
	switch len(s) {
	case hash.HexSize:
		_, err := hex.DecodeString(s)
		return err == nil
	default:
		return false
	}
}
