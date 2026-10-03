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
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"os"

	"github.com/edsrzf/mmap-go"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
)

const (
	idxMagic          = uint32(0xff744f63) // '\xfftOc'
	sha1Length        = 20
	checksumLength    = 20
	fanoutEntrySize   = 4
	crc32EntrySize    = 4
	offset32EntrySize = 4
	offset64EntrySize = 8
	indexHeaderSize   = 4 + 4 // Magic + Version
)

// IndexFile holds the parsed data from a .idx file
type IndexFile struct {
	ObjectCount   int
	ObjectMap     []map[Hash]int
	ObjectCRCs    []uint32
	ObjectOffsets []int64
	PackChecksum  Hash
	IndexChecksum Hash
}

type FindObjectResult struct {
	Offset int64
	CRC    uint32
	Index  int
}

func NewIndexFile() *IndexFile {
	objMap := make([]map[Hash]int, 256)
	for i := range objMap {
		objMap[i] = make(map[Hash]int)
	}
	return &IndexFile{
		ObjectMap: objMap,
	}
}

func ParseIndexFile(filePath string) (idx *IndexFile, retErr error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open index file '%s': %w", filePath, err)
	}
	defer entities.CloseErrCheck(file, &retErr)

	mmapData, err := mmap.Map(file, mmap.RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to mmap index file '%s': %w", filePath, err)
	}
	defer func() {
		if unmapErr := mmapData.Unmap(); unmapErr != nil {
			fmt.Printf("Warning: failed to unmap index file '%s': %v\n", filePath, unmapErr)
		}
	}()

	return ParseIndex(mmapData)
}

func ParseIndex(buf []byte) (*IndexFile, error) {
	fileSize := len(buf)

	if minSize := indexHeaderSize + 256*fanoutEntrySize + 2*checksumLength; fileSize < minSize {
		return nil, fmt.Errorf("index file is too small (%d bytes)", fileSize)
	}

	idx := NewIndexFile()

	// Read Header (Magic + Version)
	if magic := binary.BigEndian.Uint32(buf[0:4]); magic != idxMagic {
		return nil, fmt.Errorf("invalid index file magic number: expected %x, got %x", idxMagic, magic)
	}
	if ver := binary.BigEndian.Uint32(buf[4:8]); ver != 2 {
		return nil, fmt.Errorf("unsupported index file version: %d (only v2 supported)", ver)
	}

	// Read Fanout Table & Load Hashes
	fanoutEndOffset := indexHeaderSize + 256*fanoutEntrySize
	fanoutBytes := buf[indexHeaderSize:fanoutEndOffset]

	group := new(errgroup.Group)
	sha1ListBytes := buf[fanoutEndOffset:]
	for i := range 256 {
		start := idx.ObjectCount
		idx.ObjectCount = int(binary.BigEndian.Uint32(fanoutBytes[i*fanoutEntrySize : (i+1)*fanoutEntrySize]))
		count := idx.ObjectCount - start
		group.Go(func() error {
			for j := range count {
				s := (start + j) * sha1Length
				var hash Hash
				copy(hash[:], sha1ListBytes[s:s+sha1Length])
				idx.ObjectMap[i][hash] = start + j
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}

	sha1ListEnd := fanoutEndOffset + idx.ObjectCount*sha1Length

	if idx.ObjectCount == 0 {
		fmt.Printf("Warning: Index file contains 0 objects.\n")
	}

	// --- Calculate expected offsets of remaining sections ---
	crc32ListOffset := sha1ListEnd
	crc32ListSize := idx.ObjectCount * crc32EntrySize
	offset32ListOffset := crc32ListOffset + crc32ListSize
	offset32ListSize := idx.ObjectCount * offset32EntrySize
	endOfOffset32List := offset32ListOffset + offset32ListSize
	// Checksums start 40 bytes from the end of the file
	checksumsOffset := fileSize - 2*checksumLength

	// Basic size check based on calculated offsets
	if endOfOffset32List > checksumsOffset {
		return nil, fmt.Errorf("index file structure mismatch or corruption: calculated end of 32-bit offsets (%d) overlaps with checksums starting at (%d)", endOfOffset32List, checksumsOffset)
	}

	// --- 4. Read CRC32 List ---
	idx.ObjectCRCs = make([]uint32, idx.ObjectCount)
	if fileSize < crc32ListOffset+crc32ListSize {
		return nil, errors.New("index file too small for CRC32 list")
	}
	crc32Bytes := buf[crc32ListOffset : crc32ListOffset+crc32ListSize]
	for i := 0; i < idx.ObjectCount; i++ {
		idx.ObjectCRCs[i] = binary.BigEndian.Uint32(crc32Bytes[i*crc32EntrySize : (i+1)*crc32EntrySize])
	}

	// --- 5. Read 32-bit Offset List (and identify large offsets) ---
	var largeOffsetIndices []uint32                    // Stores indices into the 64-bit table
	var isLargeOffset []bool                           // Tracks which entries need 64-bit resolution
	idx.ObjectOffsets = make([]int64, idx.ObjectCount) // Final resolved offsets

	if fileSize < offset32ListOffset+offset32ListSize {
		return nil, errors.New("index file too small for 32-bit offset list")
	}
	offset32Bytes := buf[offset32ListOffset : offset32ListOffset+offset32ListSize]

	largeOffsetCount := 0
	isLargeOffset = make([]bool, idx.ObjectCount)
	for i := 0; i < idx.ObjectCount; i++ {
		offset32 := binary.BigEndian.Uint32(offset32Bytes[i*offset32EntrySize : (i+1)*offset32EntrySize])
		if offset32&0x80000000 != 0 { // Check MSB (large offset flag)
			largeOffsetIndex := offset32 & 0x7fffffff // Lower 31 bits are index
			largeOffsetIndices = append(largeOffsetIndices, largeOffsetIndex)
			isLargeOffset[i] = true
			largeOffsetCount++
		} else {
			idx.ObjectOffsets[i] = int64(offset32)
			isLargeOffset[i] = false
		}
	}
	cur := endOfOffset32List // Update current offset

	// --- 6. Read 64-bit Offset Table (if necessary) ---
	offset64ListOffset := cur // Starts right after 32-bit list
	offset64ListSize := largeOffsetCount * offset64EntrySize
	endOfOffset64List := offset64ListOffset + offset64ListSize

	// Check if 64-bit table overlaps checksums
	if endOfOffset64List > checksumsOffset {
		return nil, fmt.Errorf("index file structure mismatch or corruption: calculated end of 64-bit offsets (%d) overlaps with checksums starting at (%d)", endOfOffset64List, checksumsOffset)
	}
	// Check if calculated end matches where checksums start
	if endOfOffset64List != checksumsOffset {
		// Allow mismatch only if there are no large offsets
		if largeOffsetCount > 0 {
			return nil, fmt.Errorf("index file structure mismatch: calculated end of 64-bit offsets (%d) does not match start of checksums (%d)", endOfOffset64List, checksumsOffset)
		} else if cur != checksumsOffset {
			// If no large offsets, the 32-bit table should end right before checksums
			return nil, fmt.Errorf("index file structure mismatch: end of 32-bit offsets (%d) does not match start of checksums (%d) when no large offsets exist", cur, checksumsOffset)
		}
	}

	if largeOffsetCount > 0 {
		if fileSize < offset64ListOffset+offset64ListSize {
			return nil, errors.New("index file too small for 64-bit offset list")
		}
		offset64Bytes := buf[offset64ListOffset : offset64ListOffset+offset64ListSize]

		largeOffsetMap := make(map[uint32]int64, largeOffsetCount) // Preallocate map
		for i := 0; i < largeOffsetCount; i++ {
			offset64 := binary.BigEndian.Uint64(offset64Bytes[i*offset64EntrySize : (i+1)*offset64EntrySize])
			if i >= len(largeOffsetIndices) {
				return nil, fmt.Errorf("internal error: mismatch between large offset count and indices read")
			}
			// Store the actual 64-bit offset mapped by the *index* read from the 32-bit table.
			largeOffsetMap[largeOffsetIndices[i]] = int64(offset64)
		}

		// Resolve the large offsets in the final ObjectOffsets slice
		largeOffsetReadIndex := 0
		for i := 0; i < idx.ObjectCount; i++ {
			if isLargeOffset[i] {
				offset32Raw := binary.BigEndian.Uint32(offset32Bytes[i*offset32EntrySize : (i+1)*offset32EntrySize])
				indexIn64Table := offset32Raw & 0x7fffffff
				resolvedOffset, found := largeOffsetMap[indexIn64Table]
				if !found {
					return nil, fmt.Errorf("consistency error: large offset index %d (from object %d) not found in parsed 64-bit table", indexIn64Table, i)
				}
				idx.ObjectOffsets[i] = resolvedOffset
				largeOffsetReadIndex++
			}
		}
		if largeOffsetReadIndex != largeOffsetCount {
			return nil, fmt.Errorf("consistency error: processed %d large offsets, expected %d", largeOffsetReadIndex, largeOffsetCount)
		}
		cur = offset64ListOffset + offset64ListSize
	}

	if cur+2*checksumLength > fileSize {
		return nil, errors.New("index file too small for index and pack checksums")
	}
	idx.PackChecksum = Hash(buf[cur : cur+checksumLength])
	cur += checksumLength

	idx.IndexChecksum = Hash(buf[cur : cur+checksumLength])
	cur += checksumLength

	if cur != fileSize {
		return nil, fmt.Errorf("index file has extra data at the end (read: %d, file-size: %d)", cur, fileSize)
	}
	return idx, nil
}

func (idx *IndexFile) FindObject(oid Hash) (result FindObjectResult, found bool) {
	i, ok := idx.ObjectMap[oid[0]][oid]
	if !ok {
		return result, false
	}

	return FindObjectResult{
		Offset: idx.ObjectOffsets[i],
		CRC:    idx.ObjectCRCs[i],
		Index:  int(i),
	}, true
}

func WritePackIndex(idx *IndexFile, w io.Writer, packChecksum Hash) error {
	// Reconstruct sorted hashes from ObjectMap
	sortedHashes := make([]Hash, idx.ObjectCount)
	// count check to ensure integrity
	reconstructedCount := 0
	for _, bucket := range idx.ObjectMap {
		for h, i := range bucket {
			if i < 0 || i >= idx.ObjectCount {
				return fmt.Errorf("object index %d out of bounds (count: %d)", i, idx.ObjectCount)
			}
			sortedHashes[i] = h
			reconstructedCount++
		}
	}
	if reconstructedCount != idx.ObjectCount {
		return fmt.Errorf("object map count %d does not match ObjectCount %d", reconstructedCount, idx.ObjectCount)
	}

	h := sha1.New()
	mw := io.MultiWriter(w, h)

	// 1. Header
	if err := binary.Write(mw, binary.BigEndian, idxMagic); err != nil {
		return err
	}
	if err := binary.Write(mw, binary.BigEndian, uint32(2)); err != nil {
		return err
	}

	// 2. Fanout
	// Cumulative count of objects with first byte <= k
	fanoutCount := uint32(0)
	for i := 0; i < 256; i++ {
		fanoutCount += uint32(len(idx.ObjectMap[i]))
		if err := binary.Write(mw, binary.BigEndian, fanoutCount); err != nil {
			return err
		}
	}

	// 3. SHA1 Table
	for _, hash := range sortedHashes {
		if _, err := mw.Write(hash[:]); err != nil {
			return err
		}
	}

	// 4. CRC32 Table
	if len(idx.ObjectCRCs) != idx.ObjectCount {
		return fmt.Errorf("CRC count %d does not match ObjectCount %d", len(idx.ObjectCRCs), idx.ObjectCount)
	}
	if err := binary.Write(mw, binary.BigEndian, idx.ObjectCRCs); err != nil {
		return err
	}

	// 5. Offset 32 Table
	// 6. Offset 64 Table (prepared during step 5)
	if len(idx.ObjectOffsets) != idx.ObjectCount {
		return fmt.Errorf("offset count %d does not match ObjectCount %d", len(idx.ObjectOffsets), idx.ObjectCount)
	}

	var largeOffsets []int64
	for _, offset := range idx.ObjectOffsets {
		if offset > 0x7FFFFFFF {
			// Large offset
			entry := uint32(0x80000000) | uint32(len(largeOffsets))
			if err := binary.Write(mw, binary.BigEndian, entry); err != nil {
				return err
			}
			largeOffsets = append(largeOffsets, offset)
		} else {
			entry := uint32(offset)
			if err := binary.Write(mw, binary.BigEndian, entry); err != nil {
				return err
			}
		}
	}

	for _, offset := range largeOffsets {
		if err := binary.Write(mw, binary.BigEndian, uint64(offset)); err != nil {
			return err
		}
	}

	// 7. Packfile Checksum
	if _, err := mw.Write(packChecksum[:]); err != nil {
		return err
	}

	// 8. Index Checksum
	if _, err := w.Write(h.Sum(nil)); err != nil {
		return err
	}

	return nil
}
