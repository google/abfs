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
	"io"

	"github.com/google/abfs/cas/hash"
)

type deepDirList struct {
	entries  []*DirEntry
	curIndex int
}

func (d *deepDirList) Close() error {
	return nil
}
func (d *deepDirList) Read() (*DirEntry, error) {
	if d.curIndex < len(d.entries) {
		out := d.entries[d.curIndex]
		d.curIndex++
		return out, nil
	}
	return nil, io.EOF
}

// CreateDeepTree from the input directory hash.
// This function will walk the directory hash (h) creating new deeptrees, the
// deeptees are stored in the store.
func CreateDeepTree(h hash.Hash, store Store, threshold int64) (hash.Hash, error) {

	entries := []*DirEntry{{
		Mode: FileModeDir(),
		Hash: h}}

	var dfs func(treeEnt *DirEntry) error

	dfs = func(treeEnt *DirEntry) error {
		reader, err := store.GetDirEntries(treeEnt.Hash)
		if err != nil {
			return err
		}
		defer reader.Close()
		treeEnt.Size++
		for {
			ent, err := reader.Read()
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				break
			}
			start := len(entries)
			child := &DirEntry{
				Hash: ent.Hash,
				Name: ent.Name,
				Mode: ent.Mode,
			}
			entries = append(entries, child)
			if ent.Mode.IsDir() {
				if err := dfs(child); err != nil {
					return err
				}
				if child.Size > threshold {
					child.Name = ""
					hash, err := store.SaveDeepTree(&deepDirList{entries: entries[start:], curIndex: 0})
					if err != nil {
						return err
					}
					entries = append(entries[:start], &DirEntry{
						Name:    ent.Name,
						Mode:    FileModeDir(),
						Hash:    hash,
						ExtType: DirObjectExtTypeDeepTree,
					})
					treeEnt.Size++
				} else {
					treeEnt.Size += child.Size
				}
			} else {
				child.Size = ent.Size
				treeEnt.Size++
			}
		}
		return nil
	}
	if err := dfs(entries[0]); err != nil {
		return nil, err
	}
	return store.SaveDeepTree(&deepDirList{entries: entries, curIndex: 0})
}
