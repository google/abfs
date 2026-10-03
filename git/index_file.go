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

package git

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
)

var (
	paddings = [][]byte{
		{},
		{0},
		{0, 0},
		{0, 0, 0},
		{0, 0, 0, 0},
		{0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 0, 0},
	}
)

func readString(r io.Reader, delim uint8) (string, error) {
	ok := true
	var out []byte
	var c uint8
	n := 0
	for ok {
		if err := binary.Read(r, binary.BigEndian, &c); err != nil {
			return "", err
		}
		if c == delim {
			ok = false
		} else {
			n++
			out = append(out, byte(c))
		}
	}
	return string(out[:n]), nil
}

func (e *IndexEntry) Write(w io.Writer) error {
	if err := binary.Write(w, binary.BigEndian, e.IndexEntryData); err != nil {
		return err
	}
	if _, err := w.Write([]byte(e.Name)); err != nil {
		return err
	}
	if _, err := w.Write(paddings[e.Padding]); err != nil {
		return err
	}
	return nil
}

func (e *CacheTreeEntry) Write(w io.Writer) error {
	if _, err := w.Write([]byte(e.Path)); err != nil {
		return err
	}
	if _, err := w.Write([]byte{0x00}); err != nil {
		return err
	}
	if _, err := w.Write([]byte(e.EntryCount)); err != nil {
		return err
	}
	if _, err := w.Write([]byte{0x20}); err != nil {
		return err
	}
	if _, err := w.Write([]byte(e.SubtreeCount)); err != nil {
		return err
	}
	if _, err := w.Write([]byte{0x0a}); err != nil {
		return err
	}
	if _, err := w.Write(e.Sha1[:]); err != nil {
		return err
	}
	return nil
}

func (c *CacheTreeEntry) Read(r io.Reader) error {
	var err error
	c.Path, err = readString(r, 0)
	if err != nil {
		return err
	}

	c.EntryCount, err = readString(r, 0x20)
	if err != nil {
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		return err
	}

	c.SubtreeCount, err = readString(r, 0xa)
	if err != nil {
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		return err
	}

	err = binary.Read(r, binary.BigEndian, &c.Sha1)
	if err != nil {
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil

}
func (index *Index) writeIndexEntries(w io.Writer) error {
	index.Header.EntriesCount = uint32(index.NumEntries())

	if err := binary.Write(w, binary.BigEndian, index.Header); err != nil {
		return err
	}

	for _, e := range index.IndexEntries {
		if err := e.Write(w); err != nil {
			return err
		}
	}

	return nil
}
func (index *Index) readIndexEntries(r io.Reader) error {
	if err := binary.Read(r, binary.BigEndian, &index.Header); err != nil {
		return err
	}
	// Validate header
	if index.Header.Signature != IndexDircHeader {
		return nil
	}
	// Read the entries
	var c uint8
	var i uint32
	for i = 0; i < index.Header.EntriesCount; i++ {
		var entry IndexEntryData
		if err := binary.Read(r, binary.BigEndian, &entry); err != nil {
			return err
		}

		filename, err := readString(r, 0)
		if err != nil {
			return err
		}
		index.AddEntry(string(filename), entry)
		padding := ((1-len(filename))%8 + 8) % 8

		for range padding {
			if err := binary.Read(r, binary.BigEndian, &c); err != nil {
				return err
			}
		}
	}
	return nil
}

func (index *Index) writeCacheTreeEntries(w io.Writer) error {
	hdr := ExtensionHeader{
		Signature: IndexTreeHeader,
		Size:      index.CacheTreeSize,
	}
	if err := binary.Write(w, binary.BigEndian, hdr); err != nil {
		return err
	}
	for _, e := range index.CacheTreeEntries {
		if err := e.Write(w); err != nil {
			return err
		}
	}
	return nil
}

func (index *Index) readCacheTree(r io.Reader) error {
	var err error

	for err == nil {
		e := &CacheTreeEntry{}
		err = e.Read(r)
		if err == nil {
			index.CacheTreeEntries = append(index.CacheTreeEntries, e)
			index.CacheTreeSize += uint32(len(e.Path)+Sha1Len+CacheTreeFillerBytes) + uint32(len(e.EntryCount)+len(e.SubtreeCount))
		}
	}
	if err == io.EOF {
		return nil
	}
	return err

}
func (index *Index) readExtensions(data []byte) error {
	totalSize := len(data)
	offset := 0
	var hdr ExtensionHeader

	for (totalSize - offset) >= ExtensionHeaderSize {
		r := bytes.NewReader(data[offset:])
		if err := binary.Read(r, binary.BigEndian, &hdr); err != nil {
			return err
		}
		er := bytes.NewReader(data[offset+ExtensionHeaderSize : offset+ExtensionHeaderSize+int(hdr.Size)])
		if hdr.Signature == IndexTreeHeader {
			if err := index.readCacheTree(er); err != nil {
				return err
			}
		}
		offset += (ExtensionHeaderSize + int(hdr.Size))
	}

	if offset != totalSize {
		return fmt.Errorf("unexpected remaining data %d (total %d)", offset, totalSize)
	}
	return nil
}

func (index *Index) Write(w io.Writer) error {
	hasher := sha1.New()
	mw := io.MultiWriter(w, hasher)
	if err := index.writeIndexEntries(mw); err != nil {
		return err
	}
	if err := index.writeCacheTreeEntries(mw); err != nil {
		return err
	}
	_, err := w.Write(hasher.Sum(nil))
	return err
}

func (index *Index) Read(r io.Reader, totalSize int64) error {
	hasher := sha1.New()

	// Last 20 bytes represent the sha of the index file
	lr := io.LimitReader(r, totalSize-Sha1Len)
	tr := io.TeeReader(lr, hasher)

	if err := index.readIndexEntries(tr); err != nil {
		return err
	}
	// Read the remaining data into buffer
	data, err := io.ReadAll(tr)
	if err != nil {
		return err
	}

	err = index.readExtensions(data)
	if err != nil {
		return err
	}

	var hash IndexSha1
	if err := binary.Read(r, binary.BigEndian, &hash); err != nil {
		return err
	}

	fShaString := hash.String()
	fCalcString := hex.EncodeToString(hasher.Sum(nil))

	if fShaString != fCalcString {
		return fmt.Errorf("SHA1 mismatch calculated %s expected %s",
			fCalcString, fShaString)
	}
	return nil
}
