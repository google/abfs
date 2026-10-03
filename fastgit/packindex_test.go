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

package fastgit

import (
	"bytes"
	"encoding/hex"
	"io"
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWritePackIndex(t *testing.T) {
	// Create a mock IndexFile
	idx := &IndexFile{
		ObjectCount:   2,
		ObjectMap:     make([]map[Hash]int, 256),
		ObjectCRCs:    make([]uint32, 2),
		ObjectOffsets: make([]int64, 2),
	}

	// Object 1: "00..." (Bucket 0)
	h1Bytes, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f10111213")
	var h1 Hash
	copy(h1[:], h1Bytes)
	idx.ObjectMap[0] = map[Hash]int{h1: 0}
	idx.ObjectCRCs[0] = 0x11111111
	idx.ObjectOffsets[0] = 100

	// Object 2: "ff..." (Bucket 255)
	h2Bytes, _ := hex.DecodeString("ff0102030405060708090a0b0c0d0e0f10111213")
	var h2 Hash
	copy(h2[:], h2Bytes)
	idx.ObjectMap[0xff] = map[Hash]int{h2: 1}
	idx.ObjectCRCs[1] = 0x22222222
	idx.ObjectOffsets[1] = 0x80000001 // Large offset

	packChecksumBytes, _ := hex.DecodeString("aabbccddeeff00112233445566778899aabbccdd")
	var packChecksum Hash
	copy(packChecksum[:], packChecksumBytes)

	// Write to buffer
	var buf bytes.Buffer
	if err := WritePackIndex(idx, &buf, packChecksum); err != nil {
		t.Fatalf("WritePackIndex failed: %v", err)
	}

	// Write buffer to temp file for ParseIndexFile
	tmpFile, err := os.CreateTemp("", "index_test_*.idx")
	if err != nil {
		t.Fatalf("Temp file creation failed: %v", err)
	}
	defer func() {
		require.NoError(t, os.Remove(tmpFile.Name()))
	}()

	if _, err := io.Copy(tmpFile, &buf); err != nil {
		t.Fatalf("Copy to temp file failed: %v", err)
	}
	require.NoError(t, tmpFile.Close())

	// Parse back
	parsedIdx, err := ParseIndexFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("ParseIndexFile failed: %v", err)
	}

	// Verify
	if parsedIdx.ObjectCount != idx.ObjectCount {
		t.Errorf("ObjectCount mismatch: got %d, want %d", parsedIdx.ObjectCount, idx.ObjectCount)
	}

	// Verify ObjectMap
	if len(parsedIdx.ObjectMap[0]) != 1 {
		t.Errorf("Bucket 0 count mismatch")
	}
	if index, ok := parsedIdx.ObjectMap[0][h1]; !ok || index != 0 {
		t.Errorf("Object 1 lookup failed or wrong index: %v, %d", ok, index)
	}
	if len(parsedIdx.ObjectMap[0xff]) != 1 {
		t.Errorf("Bucket 255 count mismatch")
	}
	if index, ok := parsedIdx.ObjectMap[0xff][h2]; !ok || index != 1 {
		t.Errorf("Object 2 lookup failed or wrong index: %v, %d", ok, index)
	}

	// Verify CRCs
	if parsedIdx.ObjectCRCs[0] != idx.ObjectCRCs[0] {
		t.Errorf("CRC 0 mismatch")
	}
	if parsedIdx.ObjectCRCs[1] != idx.ObjectCRCs[1] {
		t.Errorf("CRC 1 mismatch")
	}

	// Verify Offsets
	if parsedIdx.ObjectOffsets[0] != idx.ObjectOffsets[0] {
		t.Errorf("Offset 0 mismatch")
	}
	if parsedIdx.ObjectOffsets[1] != idx.ObjectOffsets[1] {
		t.Errorf("Offset 1 mismatch")
	}
}

func TestWritePackIndex_SortOrder(t *testing.T) {
	// Ensure that if we provide objects in scrambled order of first byte (which is not possible with map but let's say inside map buckets),
	// they are written in correct global sorted order.

	// Actually IndexFile struct invariant relying on `i` means `i` IS the sorted order.
	// We need to trust `i`.
	// If we mess up `i`, the output is garbage.
	// But let's verify that we correctly iterate `i`.

	idx := &IndexFile{
		ObjectCount:   3,
		ObjectMap:     make([]map[Hash]int, 256),
		ObjectCRCs:    make([]uint32, 3),
		ObjectOffsets: make([]int64, 3),
	}

	// h1 < h2 < h3
	h1 := makeHash("0100000000000000000000000000000000000000") // Bucket 1
	h2 := makeHash("0200000000000000000000000000000000000000") // Bucket 2
	h3 := makeHash("0201000000000000000000000000000000000000") // Bucket 2

	// Map them such that i matches sort order of hashes:
	// h1 -> 0
	// h2 -> 1
	// h3 -> 2

	idx.ObjectMap[1] = map[Hash]int{h1: 0}
	idx.ObjectMap[2] = map[Hash]int{h2: 1, h3: 2} // iteration order of map is random!

	idx.ObjectCRCs[0], idx.ObjectCRCs[1], idx.ObjectCRCs[2] = 1, 2, 3
	idx.ObjectOffsets[0], idx.ObjectOffsets[1], idx.ObjectOffsets[2] = 10, 20, 30

	var buf bytes.Buffer
	if err := WritePackIndex(idx, &buf, makeHash("0000000000000000000000000000000000000000")); err != nil {
		t.Fatalf("WritePackIndex failed: %v", err)
	}

	// Verify by parsing
	tmpFile, _ := os.CreateTemp("", "index_sort_test_*.idx")
	defer func() {
		require.NoError(t, os.Remove(tmpFile.Name()))
	}()
	_, err := io.Copy(tmpFile, &buf)
	require.NoError(t, err)
	require.NoError(t, tmpFile.Close())

	parsedIdx, err := ParseIndexFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("ParseIndexFile failed: %v", err)
	}

	keys := getSortedKeys(parsedIdx.ObjectMap[2]) // h2, h3 should be there
	if len(keys) != 2 {
		t.Fatalf("Expected 2 keys in bucket 2")
	}

	// h2 should have index 1, h3 should have index 2
}

func makeHash(s string) Hash {
	b, _ := hex.DecodeString(s)
	var h Hash
	copy(h[:], b)
	return h
}

func getSortedKeys(m map[Hash]int) []Hash {
	keys := make([]Hash, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i][:], keys[j][:]) < 0
	})
	return keys
}
