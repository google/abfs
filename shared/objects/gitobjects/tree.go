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

package gitobjects

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const ObjectTypeDir ObjectType = "tree"

func init() {
	objects.Factory.Register(ObjectTypeDir, func() objects.Object {
		return new(Tree)
	})
}

type Tree struct {
	GitBaseObject
	t *object.Tree
}

var _ GitObject = (*Tree)(nil)

func NewTree(t *object.Tree) *Tree {
	return &Tree{t: t}
}

func (t *Tree) GitTree() *object.Tree {
	return t.t
}

func (t *Tree) Encode() error {
	return objectGitEncode(t.Base(), ObjectTypeDir, t.t.Encode)
}

func (t *Tree) Decode(hash entities.Hash, content []byte) error {
	tree, err := DecodeTree(plumbing.Hash(hash), content)
	if err != nil {
		return err
	}
	t.t = tree
	return nil
}

func (t *Tree) Links() []entities.Hash {
	links := make([]entities.Hash, 0, len(t.t.Entries))
	for _, ent := range t.t.Entries {
		if ent.Mode == filemode.Submodule {
			// Submodules happen when there are commits in directories, but they could be from a different
			// repository/host so we need a better way to validate them. We skip them for now.
			continue
		}
		links = append(links, entities.Hash(ent.Hash))
	}
	return links
}

type jsonTree struct {
	Hash    string
	Entries []jsonTreeEntry
}

type jsonTreeEntry struct {
	Name string
	Mode string
	Hash string
}

func newJSONTree(t *object.Tree) *jsonTree {
	entries := make([]jsonTreeEntry, len(t.Entries))
	for i, e := range t.Entries {
		entries[i] = jsonTreeEntry{
			Name: e.Name,
			Mode: e.Mode.String(),
			Hash: e.Hash.String(),
		}
	}
	return &jsonTree{
		Hash:    t.Hash.String(),
		Entries: entries,
	}
}

func (t *Tree) MarshalJSON() ([]byte, error) {
	return json.Marshal(newJSONTree(t.t))
}

func DecodeTree(hash plumbing.Hash, content []byte) (*object.Tree, error) {
	t, rest, err := entities.DecodeObject(content)
	if err != nil {
		return nil, err
	}
	if entities.ObjectType(t) != ObjectTypeDir {
		return nil, fmt.Errorf("object had type %q instead of %q", t, ObjectTypeDir)
	}
	tr := &object.Tree{}
	if err := tr.Decode(newCASObject(hash, plumbing.TreeObject, rest)); err != nil {
		return nil, err
	}
	return tr, nil
}

// EncodeTree encodes a list of file entries into ABFS tree format.
func EncodeTree(entries []entities.FileEntry) ([]byte, error) {
	// Git trees must be sorted by name.
	sort.Slice(entries, func(i, j int) bool {
		a := entries[i].Name
		if entries[i].Mode == entities.FileModeDir {
			a += "/"
		}
		b := entries[j].Name
		if entries[j].Mode == entities.FileModeDir {
			b += "/"
		}
		return a < b
	})
	return EncodeSortedTree(entries)
}

func EncodeSortedTree(entries []entities.FileEntry) ([]byte, error) {
	// Encode to ABFS format: [mode] [name]\x00[20-byte hash]
	buf := bytes.NewBuffer(make([]byte, 0, objects.MaxHeaderSize+len(entries)*64))
	buf.Write(make([]byte, objects.MaxHeaderSize))

	for _, e := range entries {
		fmt.Fprintf(buf, "%o %s\x00", uint32(e.Mode), e.Name)
		buf.Write(e.Hash[:])
	}

	content, err := objects.PutHeader(string(ObjectTypeDir), buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to prepend ABFS header: %w", err)
	}

	return content, nil
}
