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
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const ObjectTypeDeepTree ObjectType = "deeptree"

type DeepTreeObject struct {
	GitBaseObject
	entities.DeepTree
}

func init() {
	objects.Factory.Register(ObjectTypeDeepTree, func() objects.Object {
		return new(DeepTreeObject)
	})
}

func (t *DeepTreeObject) Encode() error {
	buf := bytes.NewBuffer(nil)
	buf.Write(make([]byte, objects.MaxHeaderSize))

	varint := make([]byte, binary.MaxVarintLen64)
	for _, ent := range t.Entries {
		buf.Write(ent.Hash[:])
		vn := binary.PutUvarint(varint, uint64(ent.Size))
		buf.Write(varint[:vn])
		vn = binary.PutUvarint(varint, uint64(ent.Mode))
		buf.Write(varint[:vn])
		buf.WriteString(ent.Name)
		buf.WriteByte('\x00')
	}

	content, err := objects.PutHeader(string(ObjectTypeDeepTree), buf.Bytes())
	if err != nil {
		return err
	}

	t.Encoded = content
	t.Hash = sha1.Sum(content)
	return nil
}

func (t *DeepTreeObject) Decode(hash Hash, content []byte) error {
	objType, rest, err := entities.DecodeObject(content)
	if err != nil {
		return err
	}
	if objType != string(ObjectTypeDeepTree) {
		return fmt.Errorf("cannot decode %s as a deeptree", objType)
	}
	var entries []*entities.DeepEntry
	for i := 1; len(rest) > 0; i++ {
		var hash entities.Hash
		n := copy(hash[:], rest)
		if n < len(hash) {
			return fmt.Errorf("cannot read hash of entry #%d (%d bytes left)", i, n)
		}
		rest = rest[n:]
		size, n := binary.Uvarint(rest)
		if n <= 0 {
			return fmt.Errorf("cannot read size of entry #%d", i)
		}
		rest = rest[n:]
		mode, n := binary.Uvarint(rest)
		if n <= 0 {
			return fmt.Errorf("cannot read mode of entry #%d", i)
		}
		rest = rest[n:]
		z := bytes.IndexByte(rest, '\x00')
		if n < 0 {
			return fmt.Errorf("cannot read file name of entry#%d", i)
		}

		entries = append(entries, &entities.DeepEntry{
			Name: string(rest[0:z]),
			Mode: entities.FileMode(mode),
			Hash: hash,
			Size: int64(size),
		})
		rest = rest[z+1:]
	}
	t.Entries = entries
	t.Encoded = content
	t.Hash = hash
	return nil
}

func (t *DeepTreeObject) Links() []Hash {
	links := make([]Hash, 0, len(t.Entries))
	for _, ent := range t.Entries {
		if ent.Mode == entities.FileModeSubmodule {
			// Submodules happen when there are commits in directories, but they could be from a different
			// repository/host so we need a better way to validate them. We skip them for now.
			continue
		}
		links = append(links, ent.Hash)
	}
	return links
}

type jsonDeepTree struct {
	Entries []jsonDeepTreeEntry
}

type jsonDeepTreeEntry struct {
	Name string
	Mode string
	Hash string
	Size int64
}

func newJSONDeepTree(t *entities.DeepTree) *jsonDeepTree {
	entries := make([]jsonDeepTreeEntry, len(t.Entries))
	for i, e := range t.Entries {
		entries[i] = jsonDeepTreeEntry{
			Name: e.Name,
			Mode: e.Mode.String(),
			Hash: e.Hash.String(),
			Size: e.Size,
		}
	}
	return &jsonDeepTree{
		Entries: entries,
	}
}

func (t *DeepTreeObject) MarshalJSON() ([]byte, error) {
	return json.Marshal(newJSONDeepTree(&t.DeepTree))
}
