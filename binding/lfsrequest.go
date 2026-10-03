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

import "github.com/google/abfs/shared/objects"

const (
	ObjectTypeLFSRequest ObjectType = "lfs-request"
)

type LFSRequest struct {
	JSONBaseObject
	LFSOID string // git-lfs representation (does not include a header)
}

func init() {
	objects.Factory.Register(ObjectTypeLFSRequest, func() Object {
		return new(LFSRequest)
	})
}

func (l *LFSRequest) Encode() error {
	return ObjectJSONEncode(l, ObjectTypeLFSRequest)
}

func (l *LFSRequest) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(l, ObjectTypeLFSRequest, hash, content)
}

func (l *LFSRequest) Links() []Hash {
	return []Hash{}
}

const (
	ObjectTypeLFSResult ObjectType = "lfs-result"
)

type LFSResult struct {
	JSONBaseObject
	LFSRequest Hash
	ABFSOID    Hash // ABFS hash representation (does include blob header)
}

func init() {
	objects.Factory.Register(ObjectTypeLFSResult, func() Object {
		return new(LFSResult)
	})
}

func (l *LFSResult) Encode() error {
	return ObjectJSONEncode(l, ObjectTypeLFSResult)
}

func (l *LFSResult) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(l, ObjectTypeLFSResult, hash, content)
}

func (l *LFSResult) Links() []Hash {
	return []Hash{l.LFSRequest, l.ABFSOID}
}
