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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashTextMarshal(t *testing.T) {
	require := require.New(t)
	h := EmptyFileHash
	b, err := h.MarshalText()
	require.NoError(err, "error marshaling hash")
	require.Equal(h.String(), string(b), "marshaled hash should equal original")

	var h2 Hash
	err = h2.UnmarshalText(b)
	require.NoError(err, "error unmarshaling hash")
	require.Equal(h, h2, "unmarshaled hash should equal original")

	bZero, err := ZeroHash.MarshalText()
	require.NoError(err)
	require.Equal("", string(bZero))
	err = h2.UnmarshalText(bZero)
	require.NoError(err)
	require.Equal(ZeroHash, h2)
}

func TestSHA256TextMarshal(t *testing.T) {
	require := require.New(t)
	sha := NewSHA256("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	b, err := sha.MarshalText()
	require.NoError(err)
	require.Equal(sha.String(), string(b))

	var sha2 SHA256
	err = sha2.UnmarshalText(b)
	require.NoError(err)
	require.Equal(sha, sha2)

	bZero, err := ZeroSHA256.MarshalText()
	require.NoError(err)
	require.Equal("", string(bZero))
	err = sha2.UnmarshalText(bZero)
	require.NoError(err)
	require.Equal(ZeroSHA256, sha2)
}
