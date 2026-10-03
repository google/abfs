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

package objects

import (
	"github.com/google/abfs/shared/entities"
)

const (
	CurrentDeepTreeRequestVersion                     = "0.2"
	ObjectTypeDeepTreeRequest     entities.ObjectType = "deeptree-request"
)

type DeepTreeRequest struct {
	JSONBaseObject
	Version string
	Tree    Hash
}

func init() {
	Factory.Register(ObjectTypeDeepTreeRequest, func() Object {
		return new(DeepTreeRequest)
	})
}

func (e *DeepTreeRequest) Encode() error {
	return ObjectJSONEncode(e, ObjectTypeDeepTreeRequest)
}

func (e *DeepTreeRequest) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(e, ObjectTypeDeepTreeRequest, hash, content)
}

func (e *DeepTreeRequest) Links() []Hash {
	return []Hash{e.Tree}
}
