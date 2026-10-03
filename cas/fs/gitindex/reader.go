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
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const (
	// indexHeaderSize is the size of the DIRC header: signature, version, entry count.
	indexHeaderSize = 12
	// indexEntrySize is the fixed portion of an index entry preceding the path.
	indexEntrySize = 62
	// indexChecksumSize is the size of the trailing SHA-1 over all preceding content.
	indexChecksumSize = 20
	// extensionHeaderSize is a 4-byte signature plus a 4-byte big-endian length.
	extensionHeaderSize = 8
)

// DumpResult contains the parsed index plus low-level metadata for debugging.
type DumpResult struct {
	*Result
	Version  uint32
	Checksum [20]byte

	// UnsupportedExtensions lists mandatory extension signatures
	// that were encountered but not understood. Their presence
	// means Result is incomplete
	UnsupportedExtensions []string
}

// Read parses an on-disk Git index file into a Result struct. The trailing
// SHA-1 checksum is verified, and an index carrying a mandatory extension this
// package does not implement is rejected, since the parsed entries would be
// incomplete.
func Read(indexPath string) (*Result, error) {
	dump, err := Dump(indexPath)
	if err != nil {
		return nil, err
	}
	if len(dump.UnsupportedExtensions) > 0 {
		return nil, fmt.Errorf("unsupported mandatory index extension(s): %s",
			strings.Join(dump.UnsupportedExtensions, ", "))
	}
	return dump.Result, nil
}

// Dump parses the index and extracts low-level headers and checksums for
// debugging and CLI tools. It verifies the trailing SHA-1 checksum.
func Dump(indexPath string) (retRes *DumpResult, retErr error) {
	f, err := os.Open(indexPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		cerr := f.Close()
		if retErr == nil {
			retErr = cerr
		}
	}()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	// The trailing checksum covers everything before it, so it must be excluded
	// from the hashed region.
	contentLen := fi.Size() - indexChecksumSize
	if contentLen < indexHeaderSize {
		return nil, fmt.Errorf("index file too small: %d bytes", fi.Size())
	}

	hasher := sha1.New()
	br := bufio.NewReaderSize(io.TeeReader(io.LimitReader(f, contentLen), hasher), 256*1024)

	var header [indexHeaderSize]byte
	if _, err := io.ReadFull(br, header[:]); err != nil {
		return nil, fmt.Errorf("failed to read index header: %w", err)
	}

	if string(header[0:4]) != IndexSignature {
		return nil, fmt.Errorf("bad git index signature: %q", string(header[0:4]))
	}

	version := binary.BigEndian.Uint32(header[4:8])
	if version != IndexVersion {
		return nil, fmt.Errorf("unsupported git index version %d", version)
	}

	count := binary.BigEndian.Uint32(header[8:12])

	res := &Result{
		Entries: make([]IndexEntry, count),
		Trees:   make([]*TreeEntry, 0),
	}
	var unsupported []string

	var entryBuf [indexEntrySize]byte
	for i := uint32(0); i < count; i++ {
		if _, err := io.ReadFull(br, entryBuf[:]); err != nil {
			return nil, fmt.Errorf("failed to read entry %d: %w", i, err)
		}

		ent := &res.Entries[i]
		ent.CtimeSec = binary.BigEndian.Uint32(entryBuf[0:4])
		ent.CtimeNsec = binary.BigEndian.Uint32(entryBuf[4:8])
		ent.MtimeSec = binary.BigEndian.Uint32(entryBuf[8:12])
		ent.MtimeNsec = binary.BigEndian.Uint32(entryBuf[12:16])
		ent.Dev = binary.BigEndian.Uint32(entryBuf[16:20])
		ent.Ino = binary.BigEndian.Uint32(entryBuf[20:24])
		ent.Mode = binary.BigEndian.Uint32(entryBuf[24:28])
		ent.Uid = binary.BigEndian.Uint32(entryBuf[28:32])
		ent.Gid = binary.BigEndian.Uint32(entryBuf[32:36])
		ent.Size = binary.BigEndian.Uint32(entryBuf[36:40])
		copy(ent.Sha[:], entryBuf[40:60])
		flags := binary.BigEndian.Uint16(entryBuf[60:62])

		// Path lengths of 0xFFF or greater are not stored in the flags field and
		// must be discovered by scanning to the NUL terminator.
		nameLen := int(flags & 0x0FFF)
		var nameBytes []byte
		if nameLen < 0x0FFF {
			nameBytes = make([]byte, nameLen)
			if _, err := io.ReadFull(br, nameBytes); err != nil {
				return nil, fmt.Errorf("failed to read path for entry %d: %w", i, err)
			}
		} else {
			name, err := br.ReadBytes(0)
			if err != nil {
				return nil, fmt.Errorf("failed to read long path for entry %d: %w", i, err)
			}
			nameBytes = name[:len(name)-1]
			nameLen = len(nameBytes)
		}
		ent.Path = string(nameBytes)

		// Entries are NUL-padded so the total record length is a multiple of 8.
		// The NUL terminator is the first pad byte.
		consumed := indexEntrySize + nameLen
		padLen := 8 - (consumed % 8)
		if nameLen >= 0x0FFF {
			// ReadBytes(0) already consumed the NUL terminator.
			padLen--
		}
		if padLen > 0 {
			var pad [8]byte
			if _, err := io.ReadFull(br, pad[:padLen]); err != nil {
				return nil, fmt.Errorf("failed to read padding for entry %d: %w", i, err)
			}
		}
	}

	// Extensions follow the entries. Each is an 8-byte header (4-byte signature
	// plus 4-byte big-endian length) followed by its payload. Unknown extensions
	// are skipped.
	for {
		peek, err := br.Peek(extensionHeaderSize)
		if err != nil || len(peek) < extensionHeaderSize {
			break
		}

		sig := string(peek[0:4])
		if !isExtensionSig(sig) {
			break
		}

		var extHeader [extensionHeaderSize]byte
		if _, err := io.ReadFull(br, extHeader[:]); err != nil {
			return nil, fmt.Errorf("failed to read extension header: %w", err)
		}
		extLen := binary.BigEndian.Uint32(extHeader[4:8])

		extData := make([]byte, extLen)
		if _, err := io.ReadFull(br, extData); err != nil {
			return nil, fmt.Errorf("failed to read %s extension data: %w", sig, err)
		}

		switch {
		case sig == "TREE":
			trees, err := parseTreeExtension(extData)
			if err != nil {
				return nil, fmt.Errorf("failed to parse TREE extension: %w", err)
			}
			res.Trees = trees

		case !isOptionalExtension(sig):
			// A lowercase signature marks a mandatory extension. Recorded
			// rather than skipped: "link", for example, means the entries
			// above are a delta against a shared split index, so Result is
			// incomplete. Read turns this into an error.
			unsupported = append(unsupported, sig)
		}
	}

	// Drain any remaining content so it is folded into the hash, then read the
	// trailing checksum from the region excluded above.
	if _, err := io.Copy(io.Discard, br); err != nil {
		return nil, fmt.Errorf("failed to drain index content: %w", err)
	}

	var checksum [20]byte
	if _, err := f.ReadAt(checksum[:], contentLen); err != nil {
		return nil, fmt.Errorf("failed to read index checksum: %w", err)
	}

	if sum := hasher.Sum(nil); !bytes.Equal(sum, checksum[:]) {
		return nil, fmt.Errorf("index checksum mismatch: computed %x, stored %x", sum, checksum)
	}

	return &DumpResult{
		Result:                res,
		Version:               version,
		Checksum:              checksum,
		UnsupportedExtensions: unsupported,
	}, nil
}

func isASCIILetter(c byte) bool {
	return ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z')
}

// isExtensionSig reports whether sig is a well-formed index extension
// signature: exactly four ASCII letters.
func isExtensionSig(sig string) bool {
	if len(sig) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		if !isASCIILetter(sig[i]) {
			return false
		}
	}
	return true
}

// isOptionalExtension reports whether an extension may be skipped by a reader
// that does not understand it. Per the index format, a signature beginning
// with 'A'..'Z' is optional; a lowercase signature is mandatory.
func isOptionalExtension(sig string) bool {
	return len(sig) > 0 && 'A' <= sig[0] && sig[0] <= 'Z'
}

// parseTreeExtension decodes the cache-tree (TREE) extension payload. Each
// record is a NUL-terminated path, an ASCII entry count, a space, an ASCII
// subtree count, a newline, and — when the entry count is non-negative — a
// 20-byte tree SHA. An entry count of -1 marks the subtree invalid, meaning
// git must recompute it.
func parseTreeExtension(data []byte) ([]*TreeEntry, error) {
	var trees []*TreeEntry
	offset := 0
	for offset < len(data) {
		recStart := offset

		nullIdx := bytes.IndexByte(data[offset:], 0)
		if nullIdx == -1 {
			return nil, fmt.Errorf("truncated TREE extension: missing NUL terminator for path at offset %d", recStart)
		}
		name := string(data[offset : offset+nullIdx])
		offset += nullIdx + 1

		lineEnd := bytes.IndexByte(data[offset:], '\n')
		if lineEnd == -1 {
			return nil, fmt.Errorf("truncated TREE extension: missing newline for %q at offset %d", name, recStart)
		}
		line := data[offset : offset+lineEnd]

		spaceIdx := bytes.IndexByte(line, ' ')
		if spaceIdx == -1 {
			return nil, fmt.Errorf("malformed TREE extension record for %q at offset %d: %q", name, recStart, line)
		}
		entryCountStr := string(line[:spaceIdx])
		subtreeCountStr := string(line[spaceIdx+1:])
		offset += lineEnd + 1

		entryCount, err := strconv.Atoi(entryCountStr)
		if err != nil {
			return nil, fmt.Errorf("malformed entryCount %q in TREE extension: %w", entryCountStr, err)
		}

		subtreeCount, err := strconv.Atoi(subtreeCountStr)
		if err != nil {
			return nil, fmt.Errorf("malformed subtreeCount %q in TREE extension: %w", subtreeCountStr, err)
		}

		var treeSha [20]byte
		if entryCount >= 0 {
			if offset+20 > len(data) {
				return nil, fmt.Errorf("truncated tree SHA in TREE extension")
			}
			copy(treeSha[:], data[offset:offset+20])
			offset += 20
		}

		trees = append(trees, &TreeEntry{
			Name:         name,
			EntryCount:   entryCount,
			SubtreeCount: subtreeCount,
			Sha:          treeSha,
		})
	}
	return trees, nil
}
