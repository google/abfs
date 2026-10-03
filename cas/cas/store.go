// Copyright 2025 Google LLC
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

package cas

import (
	"fmt"
	"io"
	"sort"

	"github.com/google/abfs/cas/hash"
)

type Store interface {
	Open() error
	Close() error

	StoreReader
	StoreWriter
}

// StoreItemType types of data stored within the store
type StoreItemType int

const (
	StoreItemTypeData       StoreItemType = iota // File/symlink data
	StoreItemTypeDirList                         // Directory listing (names within single directory)
	StoreItemTypeDirEntries                      // Directory entries within a single directory
	StoreItemTypeDeepTree                        // DeepTree directory entries spanning multiple directories
)

func (t StoreItemType) String() string {
	switch t {
	case StoreItemTypeData:
		return "data"
	case StoreItemTypeDirList:
		return "directory list"
	case StoreItemTypeDirEntries:
		return "directory entry"
	case StoreItemTypeDeepTree:
		return "deeptree"
	}
	return fmt.Sprintf("unknown store type%d", t)
}

// DirEntryReader stream directory entries
type DirEntryReader interface {
	Read() (*DirEntry, error)
}

// DirEntryReaderClose stream entries and close
type DirEntryReadCloser interface {
	DirEntryReader
	Close() error
}

// DirListReader stream directory listings from the store
type DirListReader interface {
	Read() (*DirListEntry, error)
}

// DirListReadClose stream directory entries and close
type DirListReadCloser interface {
	DirListReader
	Close() error
}

// QueryInfo
type QueryInfo struct {
	Type StoreItemType
	Size int64
}

// StoreReader provides an interface to read objects from the store.
// The store is accessed via the input Hash
type StoreReader interface {
	// Query check if the hash is known and return the type
	Query(hash.Hash) (*QueryInfo, error)
	// OpenFile open read only file based on input hash
	OpenFile(hash.Hash) (io.ReadCloser, error)
	// GetSymLink read the symlink based on input hash
	GetSymLink(hash.Hash) (string, error)
	// GetDirList read the directory list based on the input hash
	GetDirList(hash.Hash) (DirListReadCloser, error)
	// GetDirEntries read the directory entries based on input hash
	GetDirEntries(hash.Hash) (DirEntryReadCloser, error)
	// GetDeepTree read the deep tree based on input hash
	GetDeepTree(hash.Hash) (DirEntryReadCloser, error)
}

// StoreWriter provides an interface to write objects to the store
// The input objects are hashed as part of the writing and this
// value is returned to the caller.
type StoreWriter interface {
	// Save save the input reader data to the store,
	Save(r io.Reader) (hash.Hash, error)
	// SaveFile save the given file with size to the store
	// Note: diff between SaveFile and Save is that Save will
	// write the data to a new file whereas SaveFile will use
	// the existing file which will be moved into the store
	SaveFile(filepath string, size int64) (hash.Hash, error)
	// SaveLink save they symlink to the store
	SaveSymLink(target string) (hash.Hash, error)
	// SaveDirList save the directory list to the store
	SaveDirList(DirListReader) (hash.Hash, error)
	// SaveDirEntries save the directory entries to the store
	SaveDirEntries(DirEntryReader) (hash.Hash, error)
	// SaveDeepTree save the deep tree to the store
	SaveDeepTree(DirEntryReader) (hash.Hash, error)
}

// DirInMemory provides DirEntryReadCloser support for entries in memory.
type DirInMemory struct {
	entries  []DirEntry
	curIndex int
}

func (d *DirInMemory) Close() error {
	return nil
}
func (d *DirInMemory) Read() (*DirEntry, error) {
	if d.curIndex < len(d.entries) {
		out := d.entries[d.curIndex]
		d.curIndex++
		return &out, nil
	}
	return nil, io.EOF
}

// NewDirInMemory create new in memory holding of a directory
func NewDirInMemory(e []DirEntry) *DirInMemory {
	// Note the directory entries must be sorted
	sort.Slice(e, func(i, j int) bool {
		return e[i].Name < e[j].Name
	})
	return &DirInMemory{entries: e, curIndex: 0}
}

// NewUnsortedDirInMemory create new in memory holding of a directory without
// changing the order of the entries
func NewUnsortedDirInMemory(e []DirEntry) *DirInMemory {
	return &DirInMemory{entries: e, curIndex: 0}
}

// DirListInMemory provides DirListReadCloser support for entries in memory.
type DirListInMemory struct {
	entries  []DirListEntry
	curIndex int
}

func (d *DirListInMemory) Close() error {
	return nil
}
func (d *DirListInMemory) Read() (*DirListEntry, error) {
	if d.curIndex < len(d.entries) {
		out := d.entries[d.curIndex]
		d.curIndex++
		return &out, nil
	}
	return nil, io.EOF
}

// NewDirListInMemory create new in memory holding of a directory
func NewDirListInMemory(e []DirListEntry) *DirListInMemory {
	// Note the directory entries must be sorted
	sort.Slice(e, func(i, j int) bool {
		return e[i].Name < e[j].Name
	})

	return &DirListInMemory{entries: e, curIndex: 0}
}

var _ DirListReadCloser = (*DirListInMemory)(nil)
var _ DirEntryReadCloser = (*DirInMemory)(nil)
