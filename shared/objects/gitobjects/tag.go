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

const ObjectTypeTag ObjectType = "tag"

func init() {
	objects.Factory.Register(ObjectTypeTag, func() objects.Object {
		return new(Tag)
	})
}

func DecodeTag(hash plumbing.Hash, content []byte) (*object.Tag, error) {
	t, rest, err := entities.DecodeObject(content)
	if err != nil {
		return nil, err
	}
	if entities.ObjectType(t) != ObjectTypeTag {
		return nil, fmt.Errorf("object had type %q instead of %q", t, ObjectTypeTag)
	}
	tag := &object.Tag{}
	if err := tag.Decode(newCASObject(hash, plumbing.TagObject, rest)); err != nil {
		return nil, err
	}
	return tag, nil
}

type Tag struct {
	GitBaseObject
	t *object.Tag
}

var _ GitObject = (*Tag)(nil)

func (t *Tag) Encode() (returnErr error) {
	return objectGitEncode(t.Base(), ObjectTypeTag, t.t.Encode)
}

func (t *Tag) Decode(hash Hash, content []byte) error {
	tag, err := DecodeTag(plumbing.Hash(hash), content)
	if err != nil {
		return err
	}
	t.t = tag
	return nil
}

func (t *Tag) Links() []Hash {
	return []Hash{Hash(t.t.Target)}
}

type jsonTag struct {
	Hash         string
	Name         string
	Tagger       object.Signature
	Message      string
	PGPSignature string
	TargetType   string
	Target       string
}

func newJSONTag(t *object.Tag) *jsonTag {
	return &jsonTag{
		Hash:         t.Hash.String(),
		Name:         t.Name,
		Tagger:       t.Tagger,
		Message:      t.Message,
		PGPSignature: t.PGPSignature,
		TargetType:   t.TargetType.String(),
		Target:       t.String(),
	}
}

func (t *Tag) MarshalJSON() ([]byte, error) {
	return json.Marshal(newJSONTag(t.t))
}
