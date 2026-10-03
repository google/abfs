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
	"encoding/json"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const ObjectTypeCommit ObjectType = "commit"

func init() {
	objects.Factory.Register(ObjectTypeCommit, func() objects.Object {
		return new(Commit)
	})
}

func DecodeCommit(hash plumbing.Hash, content []byte) (*object.Commit, error) {
	t, rest, err := entities.DecodeObject(content)
	if err != nil {
		return nil, err
	}
	if ObjectType(t) != ObjectTypeCommit {
		return nil, fmt.Errorf("object had type %q instead of %q", t, ObjectTypeCommit)
	}
	c := &object.Commit{}
	if err := c.Decode(newCASObject(hash, plumbing.CommitObject, rest)); err != nil {
		return nil, err
	}
	return c, nil
}

var _ GitObject = (*Commit)(nil)

type Commit struct {
	GitBaseObject
	c *object.Commit
}

func (c *Commit) Encode() error {
	return objectGitEncode(c.Base(), ObjectTypeCommit, c.c.Encode)
}

func (c *Commit) Decode(hash Hash, content []byte) error {
	com, err := DecodeCommit(plumbing.Hash(hash), content)
	if err != nil {
		return err
	}
	c.c = com
	return nil
}

func (c *Commit) Links() []Hash {
	links := []Hash{Hash(c.c.TreeHash)}
	for _, h := range c.c.ParentHashes {
		links = append(links, Hash(h))
	}
	return links
}

type jsonCommit struct {
	Hash         string
	Author       object.Signature
	Commiter     object.Signature
	MergeTag     string
	PGPSignature string
	Message      string
	TreeHash     string
	ParentHashes []string
}

func newJSONCommit(c *object.Commit) *jsonCommit {
	parentHashes := make([]string, len(c.ParentHashes))
	for i, h := range c.ParentHashes {
		parentHashes[i] = h.String()
	}
	return &jsonCommit{
		Hash:         c.Hash.String(),
		Author:       c.Author,
		Commiter:     c.Committer,
		MergeTag:     c.MergeTag,
		PGPSignature: c.PGPSignature,
		Message:      c.Message,
		TreeHash:     c.TreeHash.String(),
		ParentHashes: parentHashes,
	}
}

func (c *Commit) MarshalJSON() ([]byte, error) {
	return json.Marshal(newJSONCommit(c.c))
}
