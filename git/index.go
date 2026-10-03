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
	"encoding/hex"
	"encoding/json"
	"strconv"
)

const (
	Sha1Len              = 20 // Length in bytes
	CacheTreeFillerBytes = 3  // 1 for NUL, 1 for space, and 1 for newline
	ExtensionHeaderSize  = 8
)

var (
	IndexDircHeader = [4]byte{'D', 'I', 'R', 'C'}
	IndexTreeHeader = [4]byte{'T', 'R', 'E', 'E'}
)

type IndexSha1 [Sha1Len]byte
type IndexSignature [4]byte

type IndexHeader struct {
	Signature    IndexSignature `json:"signature"`
	Version      uint32         `json:"version"`
	EntriesCount uint32         `json:"entry_count"`
}

type IndexEntryData struct {
	CtimeSeconds uint32    `json:"ctime_secs"`
	CtimeMillis  uint32    `json:"ctime_ms"`
	MtimeSeconds uint32    `json:"mtime_secs"`
	MtimeMillis  uint32    `json:"mtime_ms"`
	Dev          uint32    `json:"dev"`
	Ino          uint32    `json:"ino"`
	Mode         uint32    `json:"mode"`
	Uid          uint32    `json:"uid"`
	Gid          uint32    `json:"gid"`
	FileSize     uint32    `json:"filesize"`
	SHA1         IndexSha1 `json:"sha1"`
	Flags        uint16    `json:"flags"`
}

type IndexEntry struct {
	IndexEntryData
	Name    string `json:"name"`
	Padding int
}

type ExtensionHeader struct {
	Signature IndexSignature
	// Size of the entries data in bytes.
	Size uint32
}

type CacheTreeEntry struct {
	// Path relative to parent directory.
	Path string `json:"path"`

	// ASCII decimal number of index entries recursively present beneath this node.
	EntryCount string `json:"entry_count"`

	// ASCII decimal number of trees directly beneath this node.
	SubtreeCount string `json:"subtree_count"`

	Sha1 IndexSha1 `json:"sha1"`
}

func (e *CacheTreeEntry) Entries() int {
	out, _ := strconv.Atoi(e.EntryCount)
	return out
}
func (e *CacheTreeEntry) Subtrees() int {
	out, _ := strconv.Atoi(e.SubtreeCount)
	return out
}

type Index struct {
	Header           IndexHeader       `json:"header"`
	IndexEntries     []IndexEntry      `json:"entries"`
	CacheTreeEntries []*CacheTreeEntry `json:"trees"`
	CacheTreeSize    uint32
}

func NewIndex(version uint32) *Index {
	return &Index{Header: IndexHeader{
		Signature:    IndexDircHeader,
		Version:      version,
		EntriesCount: 0,
	}}
}

// Create a cache tree entry and add to the index
func (index *Index) CreateCacheTreeEntry(path string, sha1 [Sha1Len]byte) *CacheTreeEntry {
	if path == ". " {
		path = ""
	}
	cacheTreeEntry := &CacheTreeEntry{
		Path: path,
		Sha1: sha1,
	}

	index.CacheTreeEntries = append(index.CacheTreeEntries, cacheTreeEntry)
	index.CacheTreeSize += uint32(len(path) + Sha1Len + CacheTreeFillerBytes)
	return cacheTreeEntry
}

func (index *Index) NumEntries() int {
	return len(index.IndexEntries)
}

func (index *Index) AddEntry(name string, data IndexEntryData) {
	index.IndexEntries = append(index.IndexEntries,
		IndexEntry{
			IndexEntryData: data,
			Name:           name,
			Padding:        ((1-len(name))%8+8)%8 + 1,
		})
}

func (index *Index) UpdateTree(cacheTreeEntry *CacheTreeEntry, numEntries, numSubtrees int) {
	cacheTreeEntry.EntryCount = strconv.Itoa(numEntries)
	cacheTreeEntry.SubtreeCount = strconv.Itoa(numSubtrees)

	index.CacheTreeSize += uint32(len(cacheTreeEntry.EntryCount) + len(cacheTreeEntry.SubtreeCount))
}

func (i *IndexSha1) String() string {
	return hex.EncodeToString(i[:])
}
func (i *IndexSha1) MarshalJSON() ([]byte, error) {
	return json.Marshal(i.String())
}
func (i *IndexSignature) String() string {
	return string(i[:])
}
func (i *IndexSignature) MarshalJSON() ([]byte, error) {
	return json.Marshal(i.String())
}
