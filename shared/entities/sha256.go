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
	"encoding/json"
	"fmt"

	"github.com/google/abfs/third_party/go-git/plumbing"
)

var _ json.Marshaler = (*SHA256)(nil)
var _ json.Unmarshaler = (*SHA256)(nil)

var _ encoding.TextMarshaler = (*SHA256)(nil)
var _ encoding.TextUnmarshaler = (*SHA256)(nil)

// The basic SHA-256 helpers below delegate to the go-git-derived code in
// third_party/go-git.

const (
	// SHA256Size is the number of bytes in a SHA256.
	SHA256Size = plumbing.SHA256Size
	// SHA256HexSize is the length of a SHA256 in hexadecimal notation.
	SHA256HexSize = plumbing.SHA256HexSize
)

// SHA256 is a SHA-256 content hash.
type SHA256 [SHA256Size]byte

var (
	ZeroSHA256 = SHA256(plumbing.ZeroSHA256)
)

// String returns h in hexadecimal notation.
func (h SHA256) String() string {
	return plumbing.SHA256(h).String()
}

// IsZero reports whether h is the zero hash.
func (h SHA256) IsZero() bool {
	return plumbing.SHA256(h).IsZero()
}

// NewSHA256 parses a hexadecimal SHA-256 hash. Invalid input yields a zero or
// partial hash; use IsSHA256 to validate first.
func NewSHA256(s string) SHA256 {
	return SHA256(plumbing.NewSHA256(s))
}

// IsSHA256 reports whether s is a valid hexadecimal SHA-256 hash.
func IsSHA256(s string) bool {
	return plumbing.IsSHA256(s)
}

// MarshalJSON implements the json.Marshaler interface for SHA256.
func (h SHA256) MarshalJSON() ([]byte, error) {
	if h.IsZero() {
		return json.Marshal("")
	}
	return json.Marshal(h.String())
}

// UnmarshalJSON implements the json.Unmarshaler interface for SHA256.
func (h *SHA256) UnmarshalJSON(data []byte) error {
	var hex string
	if err := json.Unmarshal(data, &hex); err != nil {
		return err
	}
	if hex == "" {
		*h = ZeroSHA256
		return nil
	}
	if !IsSHA256(hex) {
		return fmt.Errorf("invalid SHA256 %q", hex)
	}
	*h = NewSHA256(hex)
	return nil
}

// MarshalText implements encoding.TextMarshaler
func (h SHA256) MarshalText() ([]byte, error) {
	if h.IsZero() {
		return []byte(""), nil
	}
	return []byte(h.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler
func (h *SHA256) UnmarshalText(text []byte) error {
	hexStr := string(text)
	if hexStr == "" {
		*h = ZeroSHA256
		return nil
	}
	if !IsSHA256(hexStr) {
		return fmt.Errorf("invalid SHA256 %q", hexStr)
	}
	*h = NewSHA256(hexStr)
	return nil
}
