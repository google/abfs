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

package binding

import (
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const (
	CurrentPatchVersion            = "0.1"
	ObjectTypePatch     ObjectType = "patch"
)

type PatchObject struct {
	JSONBaseObject
	// we can't embed entities.Patch into this object because then the JSON Marshaler
	// just calls entities.Patch.MarshalJSON() and ignores the Version field
	Patch   entities.Patch
	Version string
}

func init() {
	objects.Factory.Register(ObjectTypePatch, func() Object {
		return new(PatchObject)
	})
}

func (e *PatchObject) Encode() error {
	return ObjectJSONEncode(e, ObjectTypePatch)
}

func (e *PatchObject) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(e, ObjectTypePatch, hash, content)
}

func (e *PatchObject) Links() []Hash {
	links := make([]Hash, 0, len(e.Patch))
	for _, ent := range e.Patch {
		if ent.Hash == entities.ZeroHash {
			continue
		}
		links = append(links, ent.Hash)
	}
	return links
}
