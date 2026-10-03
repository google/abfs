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

package fscas

import (
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
)

type dirDeepTree struct {
	d *dir
	h hash.Hash
}

type dirDeepTreeList []*dirDeepTree

func (l *dirDeepTreeList) add(d *dir, h hash.Hash) {
	*l = append(*l, &dirDeepTree{d, h})
}

// Set a deeptree element for the diectory.
// For the number of entries contained within hte deeptree object entry
// create the file system objects found.  If a deeptree is found an
// empty directory will be created and the element will be added to
// the input list to be handled later.
func (d *dir) setDeepTree(e *cas.DirEntry, tree cas.DirEntryReadCloser, list dirDeepTreeList) error {
	d.Lock()
	defer d.Unlock()

	d.InitHash(e.Hash)

	hasher := d.FS().Hasher()
	entryCount := e.Size
	var i int64
	for i = 1; i < entryCount; i++ {
		ent, err := tree.Read()
		if err != nil {
			return err
		}
		// Create entry if we have a deep tree clear
		// out hash for new entry
		hash := ent.Hash
		isDir := ent.Mode.IsDir()
		deepTree, subModule := false, false
		if isDir {
			deepTree = ent.ExtType == cas.DirObjectExtTypeDeepTree
			subModule = ent.ExtType == cas.DirObjectExtTypeSubModule
		}
		// Create directory for deeptree, directory, or commit type
		if deepTree {
			// Override hash
			ent.Hash = hasher.EmptyDirHash()
		}
		casNode, err := d.createNewCASNode(ent)
		if err != nil {
			return err
		}
		if deepTree {
			list.add(casNode.(*dir), hash)

		} else if subModule {
		} else if isDir {
			i += (ent.Size - 1)
			err := casNode.(*dir).setDeepTree(ent, tree, list)
			if err != nil {
				return err
			}
		}
	}
	d.isLoaded = true
	return nil
}

// Populate the directory based on the input deeptree hash.
// Open the deeptree file from the cache and iterate the
// elements and creating the directory elements.  A deeptree
// can be composed of sub-deeptrees these will be added to
// the linked list (dirDeepTreeList) when encountered and
// resolved here as well.
func (d *dir) SetDeepTree(hash hash.Hash) error {
	var head dirDeepTreeList

	head.add(d, hash)
	for _, o := range head {
		tree, err := GetStore(d.FS()).GetDeepTree(o.h)
		if err != nil {
			return err
		}
		defer tree.Close()
		ent, err := tree.Read()
		if err != nil {
			return err
		}
		if err := o.d.setDeepTree(ent, tree, head); err != nil {
			return err
		}
	}
	return nil
}
