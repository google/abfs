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
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/edsrzf/mmap-go"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

type Pack struct {
	path    string
	index   *IndexFile
	pack    *os.File
	packMem mmap.MMap
	packLen int64
}

type ObjectType uint8

const (
	ObjectCommit   ObjectType = 1
	ObjectTree     ObjectType = 2
	ObjectBlob     ObjectType = 3
	ObjectTag      ObjectType = 4
	ObjectOfsDelta ObjectType = 6
	ObjectRefDelta ObjectType = 7
)

type ResolvedObject struct {
	Type ObjectType
	Data []byte
}

func (t ObjectType) String() string {
	switch t {
	case ObjectCommit:
		return "commit"
	case ObjectTree:
		return "tree"
	case ObjectBlob:
		return "blob"
	case ObjectTag:
		return "tag"
	default:
		return fmt.Sprintf("unknown (%d)", t)
	}
}

const maxDeltaChainDepth = 50

func openPack(path string) (*Pack, error) {
	idx, err := ParseIndexFile(path + ".idx")
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path + ".pack")
	if err != nil {
		return nil, err
	}

	packMMap, err := mmap.Map(f, mmap.RDONLY, 0)
	if err != nil {
		if err := f.Close(); err != nil {
			log.Printf("Failed to close file: %s", err)
		}
		return nil, fmt.Errorf("failed to mmap packfile '%s': %w", path+".pack", err)
	}

	return &Pack{
		path:    path,
		index:   idx,
		pack:    f,
		packMem: packMMap,
		packLen: int64(len(packMMap)),
	}, nil
}

func (p *Pack) Close() error {
	var err error

	if p.packMem != nil {
		if err2 := p.packMem.Unmap(); err2 != nil {
			err = err2
		}
	}

	if p.pack != nil {
		if err2 := p.pack.Close(); err2 != nil && err == nil {
			err = err2
		}
	}

	return err
}

func (p *Pack) OpenObject(oid Hash) (io.ReadCloser, error) {
	result, err := p.resolveObject(oid, 0)
	if err != nil {
		return nil, err
	}

	reader := objects.PrependHeaderReader(bytes.NewReader(result.Data), result.Type.String(), int64(len(result.Data)))

	return io.NopCloser(reader), nil
}

func (p *Pack) resolveObject(oid Hash, depth int) (*ResolvedObject, error) {
	if depth > maxDeltaChainDepth {
		return nil, fmt.Errorf("max delta chain depth (%d) exceeded for %s", maxDeltaChainDepth, oid)
	}
	ent, r := p.index.FindObject(oid)
	if !r {
		return nil, entities.NotExistErrf("cannot find object %s", oid)
	}
	t, buf, err := p.readPackEntry(ent.Offset, depth)
	if err != nil {
		return nil, fmt.Errorf("failed to read pack entry for %s at offset %d: %w", oid, ent.Offset, err)
	}
	return &ResolvedObject{Type: t, Data: buf}, nil
}

func (p *Pack) readPackEntry(entryStartOffset int64, depth int) (resolvedType ObjectType, resolvedData []byte, err error) {
	cur := entryStartOffset

	if cur < 0 || cur >= p.packLen {
		return 0, nil, fmt.Errorf("initial offset %d out of bounds for packfile size %d", cur, p.packLen)
	}

	objType, uncompSize, bytesRead, err := readPackTypeAndSize(p.packMem[cur:])
	if err != nil {
		return 0, nil, fmt.Errorf("readPackEntry(offset %d): failed reading type/size: %w", entryStartOffset, err)
	}
	cur += int64(bytesRead)

	var baseObjectData []byte
	var baseObjectType ObjectType

	switch objType {
	case ObjectOfsDelta:
		if cur >= p.packLen {
			return 0, nil, errors.New("packfile ended prematurely before OFS_DELTA offset")
		}
		negOffset, bytesRead, err := readVariableLengthInt(p.packMem[cur:])
		if err != nil {
			return 0, nil, fmt.Errorf("OFS_DELTA base offset read: %w", err)
		}
		cur += int64(bytesRead)
		baseObjectAbsOffset := entryStartOffset - negOffset
		baseObjectType, baseObjectData, err = p.readPackEntry(baseObjectAbsOffset, depth+1)
		if err != nil {
			return 0, nil, fmt.Errorf("OFS_DELTA base object at %d: %w", baseObjectAbsOffset, err)
		}

	case ObjectRefDelta:
		if cur+sha1Length > p.packLen {
			return 0, nil, errors.New("packfile ended prematurely before REF_DELTA SHA1")
		}
		var baseHash Hash
		copy(baseHash[:], p.packMem[cur:cur+sha1Length])
		cur += sha1Length
		baseObj, err := p.resolveObject(baseHash, depth+1)
		if err != nil {
			return 0, nil, fmt.Errorf("REF_DELTA base object %s: %w", baseHash, err)
		}
		baseObjectType = baseObj.Type
		baseObjectData = baseObj.Data

	case ObjectCommit, ObjectTree, ObjectBlob, ObjectTag:
		if cur >= p.packLen {
			return 0, nil, errors.New("packfile ended prematurely before base object data")
		}
		compressedStreamReader := bytes.NewReader(p.packMem[cur:])
		decompressedData, err := decompressZlibStreamAndCount(compressedStreamReader) // We don't strictly need compressedBytesRead here
		if err != nil {
			return 0, nil, fmt.Errorf("decompressing base object: %w", err)
		}
		if int64(len(decompressedData)) != uncompSize {
			return 0, nil, fmt.Errorf("size mismatch for base object, expected %d, got %d", uncompSize, len(decompressedData))
		}
		return objType, decompressedData, nil

	default:
		return 0, nil, fmt.Errorf("unknown object type %d at offset %d", objType, entryStartOffset)
	}

	// For DELTA objects: read and apply delta instructions
	if cur >= p.packLen {
		return 0, nil, errors.New("packfile ended prematurely before delta instructions")
	}
	deltaInstructionsReader := bytes.NewReader(p.packMem[cur:])
	deltaInstructions, err := decompressZlibStreamAndCount(deltaInstructionsReader) // We don't strictly need compressedBytesRead here
	if err != nil {
		return 0, nil, fmt.Errorf("decompressing delta instructions: %w", err)
	}

	finalData, err := applyDelta(baseObjectData, deltaInstructions)
	if err != nil {
		return 0, nil, fmt.Errorf("applying delta: %w", err)
	}

	return baseObjectType, finalData, nil
}

func readPackTypeAndSize(buf []byte) (objType ObjectType, size int64, bytesRead int, err error) {
	b := buf[0]
	objType = ObjectType((b >> 4) & 0x07)
	size = int64(b & 0x0F)
	if b&0x80 == 0 {
		return objType, size, 1, nil
	}
	var shift uint = 4
	for i, b := range buf[1:] {
		v := int64(b & 0x7F)
		size |= (v << shift)
		if b&0x80 == 0 {
			return objType, size, i + 2, nil
		}
		shift += 7
		if shift >= 64 {
			break
		}
	}
	return 0, 0, 0, errors.New("var size too large")
}

func readVariableLengthInt(buf []byte) (val int64, i int, err error) {
	val = int64(buf[i]) & 0x7f
	for buf[i]&0x80 > 0 {
		i = i + 1
		val = ((val + 1) << 7) | int64(buf[i]&0x7f)
	}
	return val, i + 1, nil
}

func decompressZlibStreamAndCount(r io.Reader) (decompressedData []byte, retErr error) {
	zlibReader, err := zlib.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("zlib.NewReader: %w", err)
	}
	defer entities.CloseErrCheck(zlibReader, &retErr)

	decompressedData, err = io.ReadAll(zlibReader)
	if err != nil {
		return nil, fmt.Errorf("io.ReadAll zlib: %w", err)
	}
	return decompressedData, nil
}

func readLittleEndianBase128Number(buf []byte) (int64, int) {
	zpos := 0
	toread := int64(buf[zpos] & 0x7f)
	shift := uint64(0)
	for buf[zpos]&0x80 > 0 {
		zpos += 1
		shift += 7
		toread |= int64(buf[zpos]&0x7f) << shift
	}
	zpos += 1
	return toread, zpos
}

func applyDelta(baseData []byte, deltaData []byte) ([]byte, error) {
	expectedBaseSize, readBytes := readLittleEndianBase128Number(deltaData)
	if expectedBaseSize != int64(len(baseData)) {
		return nil, fmt.Errorf("delta base size mismatch (exp %d, got %d delta)", len(baseData), expectedBaseSize)
	}
	deltaData = deltaData[readBytes:]

	targetSize, readBytes := readLittleEndianBase128Number(deltaData)

	deltaData = deltaData[readBytes:]

	deltaBytesReader := bytes.NewReader(deltaData)
	var result bytes.Buffer
	result.Grow(int(targetSize))
	for {
		cmdByte, err := deltaBytesReader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("delta cmd byte: %w", err)
		}
		if cmdByte&0x80 != 0 {
			var offset, length uint32
			var currentVal uint32
			for i := 0; i < 4; i++ {
				if cmdByte&(1<<uint(i)) != 0 {
					b, e := deltaBytesReader.ReadByte()
					if e != nil {
						return nil, e
					}
					currentVal |= uint32(b) << (uint(i) * 8)
				}
			}
			offset = currentVal
			currentVal = 0
			for i := 0; i < 3; i++ {
				if cmdByte&(1<<uint(i+4)) != 0 {
					b, e := deltaBytesReader.ReadByte()
					if e != nil {
						return nil, e
					}
					currentVal |= uint32(b) << (uint(i) * 8)
				}
			}
			length = currentVal
			if length == 0 {
				length = 0x10000
			}
			if int(offset+length) > len(baseData) {
				return nil, fmt.Errorf("delta copy out of bounds")
			}
			result.Write(baseData[offset : offset+length])
		} else { // Insert
			insertLen := int(cmdByte & 0x7F)
			if insertLen == 0 {
				return nil, errors.New("delta insert len 0")
			}
			if _, err := io.CopyN(&result, deltaBytesReader, int64(insertLen)); err != nil {
				return nil, fmt.Errorf("delta insert data: %w", err)
			}
		}
	}
	finalData := result.Bytes()
	if int64(len(finalData)) != targetSize {
		return nil, fmt.Errorf("delta final size mismatch")
	}
	return finalData, nil
}
