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

package gitindex

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIndexRoundTrip(t *testing.T) {
	res := &Result{Entries: []IndexEntry{
		{Path: "a", Mode: 0100644},
		{Path: strings.Repeat("x/", 40) + "f.txt", Mode: 0100644},
		{Path: strings.Repeat("y/", 2100) + "deep.txt", Mode: 0100644}, // > 0xFFF
	}}

	dir := t.TempDir()
	p := filepath.Join(dir, "index")
	require.NoError(t, Write(p, res))

	got, err := Read(p) // validates the checksum
	require.NoError(t, err)
	require.Equal(t, len(res.Entries), len(got.Entries))
	for i := range res.Entries {
		require.Equal(t, res.Entries[i].Path, got.Entries[i].Path)
	}
}
func TestIndexPathLengthBoundary(t *testing.T) {
	for _, n := range []int{1, 7, 8, 9, 4094, 4095, 4096, 4097} {
		t.Run(fmt.Sprintf("len=%d", n), func(t *testing.T) {
			res := &Result{Entries: []IndexEntry{
				{Path: strings.Repeat("a", n), Mode: 0100644},
				{Path: "zzz_sentinel", Mode: 0100644}, // desync shows up here
			}}
			p := filepath.Join(t.TempDir(), "index")
			require.NoError(t, Write(p, res))

			got, err := Read(p)
			require.NoError(t, err)
			require.Len(t, got.Entries, 2)
			require.Equal(t, res.Entries[0].Path, got.Entries[0].Path)
			require.Equal(t, "zzz_sentinel", got.Entries[1].Path)
		})
	}
}
func TestReadRejectsCorruption(t *testing.T) {
	base := &Result{Entries: []IndexEntry{{Path: "a", Mode: 0100644}}}
	p := filepath.Join(t.TempDir(), "index")
	require.NoError(t, Write(p, base))
	good, err := os.ReadFile(p)
	require.NoError(t, err)

	tests := map[string]func([]byte) []byte{
		"bad signature": func(b []byte) []byte { b[0] = 'X'; return b },
		"bad version":   func(b []byte) []byte { b[7] = 4; return b },
		"flipped bit":   func(b []byte) []byte { b[20] ^= 0x01; return b },
		"truncated":     func(b []byte) []byte { return b[:len(b)-5] },
		"too small":     func(b []byte) []byte { return []byte("DIRC") },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "index")
			require.NoError(t, os.WriteFile(p, mutate(slices.Clone(good)), 0644))
			_, err := Read(p)
			require.Error(t, err)
		})
	}
}

func TestTreeExtensionRoundTrip(t *testing.T) {
	res := &Result{
		Entries: []IndexEntry{{Path: "src/a.go", Mode: 0100644}},
		Trees: []*TreeEntry{
			{Name: "", EntryCount: 2, SubtreeCount: 1, Sha: [20]byte{0x01}},
			{Name: "src", EntryCount: -1, SubtreeCount: 0}, // invalid: no SHA on wire
			{Name: "vendor", EntryCount: 5, SubtreeCount: 0, Sha: [20]byte{0x02}},
		},
	}

	p := filepath.Join(t.TempDir(), "index")
	require.NoError(t, Write(p, res))
	got, err := Read(p)
	require.NoError(t, err)

	require.Len(t, got.Trees, 3)
	require.Equal(t, -1, got.Trees[1].EntryCount)
	require.Equal(t, [20]byte{}, got.Trees[1].Sha) // must stay zero
	require.Equal(t, [20]byte{0x02}, got.Trees[2].Sha)
}

func TestParseTreeExtensionRejectsMalformed(t *testing.T) {
	valid := append([]byte("\x002 1\n"), bytes.Repeat([]byte{0xAB}, 20)...)

	tests := map[string][]byte{
		"missing NUL":     []byte("noterminator"),
		"missing newline": []byte("\x002 1"),
		"missing space":   []byte("\x0021\n"),
		"bad entry count": []byte("\x00abc 1\n"),
		"bad subtree cnt": []byte("\x002 xyz\n"),
		"truncated sha":   append([]byte("\x002 1\n"), 0xAB, 0xCD),
		"second record":   append(valid, []byte("src\x00")...), // valid, then truncated
	}

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseTreeExtension(data)
			require.Error(t, err)
			require.Nil(t, got, "must not return partial results")
		})
	}
}

func TestIsExtensionSig(t *testing.T) {
	for sig, want := range map[string]bool{
		"TREE": true,
		"REUC": true,
		"link": true, // mandatory, but still a well-formed signature
		"sdir": true,
		"TRE":  false, // too short
		"TRE1": false, // digit
		"TR E": false, // space
	} {
		require.Equal(t, want, isExtensionSig(sig), "sig=%q", sig)
	}
}

func TestIsOptionalExtension(t *testing.T) {
	require.True(t, isOptionalExtension("TREE"))
	require.True(t, isOptionalExtension("UNTR"))
	require.False(t, isOptionalExtension("link"))
	require.False(t, isOptionalExtension("sdir"))
}

func TestReadRejectsMandatoryExtension(t *testing.T) {
	res := &Result{Entries: []IndexEntry{{Path: "a", Mode: 0100644}}}
	p := filepath.Join(t.TempDir(), "index")
	require.NoError(t, Write(p, res))

	good, err := os.ReadFile(p)
	require.NoError(t, err)

	// Splice a "link" extension in ahead of the trailing checksum and
	// recompute the SHA-1 so the checksum check does not mask the result.
	payload := []byte{0x00, 0x00, 0x00, 0x00}
	ext := append([]byte("link"), 0, 0, 0, 0)
	binary.BigEndian.PutUint32(ext[4:8], uint32(len(payload)))
	ext = append(ext, payload...)

	content := append(slices.Clone(good[:len(good)-20]), ext...)
	sum := sha1.Sum(content)
	require.NoError(t, os.WriteFile(p, append(content, sum[:]...), 0644))

	// Dump still succeeds and reports the extension.
	dump, err := Dump(p)
	require.NoError(t, err)
	require.Equal(t, []string{"link"}, dump.UnsupportedExtensions)

	// Read rejects it.
	_, err = Read(p)
	require.ErrorContains(t, err, "link")
}
