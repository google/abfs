// Copyright 2018 Sourced Technologies, S.L., Google LLC
// SPDX-License-Identifier: Apache-2.0
// See the LICENSE file in third_party/go-git for the full license text.

package plumbing

import (
	"encoding/hex"
)

// The SHA256 type and its helpers below are a Google modification of the
// SHA-1 Hash type and helpers in hash.go, adapted to 32-byte SHA-256 hashes.

const (
	// SHA256Size defines the amount of bytes the SHA256 yields.
	SHA256Size = 32
	// SHA256HexSize defines the strings size of the SHA256 when represented in hexadecimal.
	SHA256HexSize = 64
)

// SHA256 SHA-256 hashed content
type SHA256 [SHA256Size]byte

// ZeroSHA256 is SHA256 with value zero
var ZeroSHA256 SHA256

// NewSHA256 return a new SHA256 from a hexadecimal hash representation
func NewSHA256(s string) SHA256 {
	b, _ := hex.DecodeString(s)

	var h SHA256
	copy(h[:], b)

	return h
}

func (h SHA256) IsZero() bool {
	var empty SHA256
	return h == empty
}

func (h SHA256) String() string {
	return hex.EncodeToString(h[:])
}

// IsSHA256 returns true if the given string is a valid SHA256.
func IsSHA256(s string) bool {
	switch len(s) {
	case SHA256HexSize:
		_, err := hex.DecodeString(s)
		return err == nil
	default:
		return false
	}
}
