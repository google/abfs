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
)

const (
	IndexSignature = "DIRC"
	IndexVersion   = 2
)

// Write serializes res into a Git index v2 file at indexPath.
// It is not atomic — the caller owns any locking or rename semantics.
// See FS.WriteGitIndex for the atomic, exclusion-aware variant.
func Write(indexPath string, res *Result) (retErr error) {
	f, err := os.OpenFile(indexPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open index file: %w", err)
	}

	defer func() {
		cerr := f.Close()
		if retErr == nil {
			retErr = cerr
		}
	}()
	return WriteTo(f, res)
}

// WriteTo serializes res into dst in Git index v2 format. The caller retains
// ownership of dst and is responsible for closing it.
func WriteTo(dst io.Writer, res *Result) error {
	hasher := sha1.New()
	bw := bufio.NewWriterSize(dst, 256*1024)
	w := io.MultiWriter(bw, hasher)

	var header [12]byte
	copy(header[0:4], IndexSignature)
	binary.BigEndian.PutUint32(header[4:8], IndexVersion)
	binary.BigEndian.PutUint32(header[8:12], uint32(len(res.Entries)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}

	var (
		entryBytes [62]byte
		zeros      [8]byte
	)

	for _, e := range res.Entries {
		binary.BigEndian.PutUint32(entryBytes[0:4], e.CtimeSec)
		binary.BigEndian.PutUint32(entryBytes[4:8], e.CtimeNsec)
		binary.BigEndian.PutUint32(entryBytes[8:12], e.MtimeSec)
		binary.BigEndian.PutUint32(entryBytes[12:16], e.MtimeNsec)
		binary.BigEndian.PutUint32(entryBytes[16:20], e.Dev)
		binary.BigEndian.PutUint32(entryBytes[20:24], e.Ino)
		binary.BigEndian.PutUint32(entryBytes[24:28], e.Mode)
		binary.BigEndian.PutUint32(entryBytes[28:32], e.Uid)
		binary.BigEndian.PutUint32(entryBytes[32:36], e.Gid)
		binary.BigEndian.PutUint32(entryBytes[36:40], e.Size)
		copy(entryBytes[40:60], e.Sha[:])

		pathLen := len(e.Path)
		flags := uint16(pathLen)
		if pathLen > 0xFFF {
			flags = 0xFFF
		}
		binary.BigEndian.PutUint16(entryBytes[60:62], flags)

		if _, err := w.Write(entryBytes[:]); err != nil {
			return err
		}

		if _, err := io.WriteString(w, e.Path); err != nil {
			return err
		}

		// 8-byte boundary alignment
		totalLen := 62 + pathLen
		padLen := 8 - (totalLen % 8)
		if _, err := w.Write(zeros[:padLen]); err != nil {
			return err
		}
	}

	if len(res.Trees) > 0 {
		treeBuf := new(bytes.Buffer)
		for _, t := range res.Trees {
			treeBuf.WriteString(t.Name)
			treeBuf.WriteByte(0)
			treeBuf.WriteString(fmt.Sprintf("%d %d\n", t.EntryCount, t.SubtreeCount))
			if t.EntryCount >= 0 {
				treeBuf.Write(t.Sha[:])
			}
		}

		treeData := treeBuf.Bytes()
		var extHeader [8]byte
		copy(extHeader[0:4], "TREE")
		binary.BigEndian.PutUint32(extHeader[4:8], uint32(len(treeData)))

		if _, err := w.Write(extHeader[:]); err != nil {
			return err
		}
		if _, err := w.Write(treeData); err != nil {
			return err
		}
	}

	if err := bw.Flush(); err != nil {
		return fmt.Errorf("failed to flush index buffer: %w", err)
	}
	checksum := hasher.Sum(nil)
	if _, err := dst.Write(checksum); err != nil {
		return fmt.Errorf("failed to write checksum: %w", err)
	}
	return nil
}
