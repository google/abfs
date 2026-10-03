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

// DiffAction indicates if an entry would need to be added or removed from hash
// to make two hashes equal
type DiffAction int

const (
	DiffActionAdd    = iota // Add item
	DiffActionRemove        // Remove item
)

func (a DiffAction) String() string {
	switch a {
	case DiffActionAdd:
		return "+"
	case DiffActionRemove:
		return "-"
	default:
		panic(fmt.Sprintf("unknown action %d", a))
	}
}

// DiffHandler provides a user callback routines when comparing directory
// entries.  For each difference encountered the callback is invoked with
// the diff action along with the attributes of the entry.
type DiffHandler = func(action DiffAction, dirPath string, de *DirEntry)

// Diff will compare two directory objects via their hash values.  If the hash values
// are the different the internal data of the directories are compared recursively.
func Diff(h1, h2 hash.Hash, store Store, showParents bool, dh DiffHandler) error {
	if h1 == nil && h2 == nil {
		return nil
	}
	if showParents && !h1.Equals(h2) {
		dh(DiffActionRemove, ".", &DirEntry{Hash: h1, Size: 0, Mode: FileModeDir(), ExtType: DirObjectExtTypeNone, Name: "."})
		dh(DiffActionAdd, ".", &DirEntry{Hash: h2, Size: 0, Mode: FileModeDir(), ExtType: DirObjectExtTypeNone, Name: "."})
	}

	return diffHashes(".", h1, h2, store, dh, showParents)
}

type diffEntries struct {
	ent1 *DirEntry
	ent2 *DirEntry
}

func createDiffMap(h1, h2 hash.Hash, store StoreReader) (map[string]*diffEntries, error) {
	entries1, err := store.GetDirEntries(h1)
	if err != nil {
		return nil, err
	}
	defer entries1.Close()
	entries2, err := store.GetDirEntries(h2)
	if err != nil {
		return nil, err
	}
	defer entries2.Close()

	entryMap := make(map[string]*diffEntries)

	for {
		e, err := entries1.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		entryMap[e.Name] = &diffEntries{ent1: e}
	}
	for {
		e, err := entries2.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if de, ok := entryMap[e.Name]; ok {
			de.ent2 = e
		} else {
			entryMap[e.Name] = &diffEntries{ent2: e}
		}
	}
	return entryMap, nil
}
func diffHashes(path string, h1, h2 hash.Hash, store StoreReader, dh DiffHandler, showParents bool) error {
	if h1.Equals(h2) {
		return nil
	}
	entryMap, err := createDiffMap(h1, h2, store)
	if err != nil {
		return err
	}
	addPathChange := func(e *diffEntries) {
		if e.ent1 != nil {
			dh(DiffActionRemove, path, e.ent1)
		}
		if e.ent2 != nil {
			dh(DiffActionAdd, path, e.ent2)
		}
	}
	keys := make([]string, 0, len(entryMap))
	for key := range entryMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ents := entryMap[k]
		if ents.ent1 == nil || ents.ent2 == nil {
			addPathChange(ents)
			continue
		}
		if ents.ent1.Mode != ents.ent2.Mode {
			addPathChange(ents)
		} else if ents.ent1.ExtType != ents.ent2.ExtType {
			addPathChange(ents)
		}
		if ents.ent1.Hash.Equals(ents.ent2.Hash) {
			continue
		}
		if !ents.ent1.Mode.IsDir() {
			addPathChange(ents)
			continue
		}
		if showParents {
			addPathChange(ents)
		}
		if err := diffHashes(path+"/"+k, ents.ent1.Hash, ents.ent2.Hash, store, dh, showParents); err != nil {
			return err
		}
	}
	return nil
}
