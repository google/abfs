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
	"encoding/json"
	"fmt"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const ObjectTypeSymRef ObjectType = "symref"

func init() {
	objects.Factory.Register(ObjectTypeSymRef, func() objects.Object {
		return new(SymRefObject)
	})
}

type SymRefObject struct {
	GitBaseObject
	Content string
}

func (t *SymRefObject) Encode() error {
	buf := bytes.NewBuffer(nil)
	_ = objects.WriteHeader(buf, string(ObjectTypeSymRef), int64(len(t.Content)))
	buf.WriteString(t.Content)

	t.Encoded = buf.Bytes()
	t.Hash = sha1.Sum(t.Encoded)
	return nil
}

func (t *SymRefObject) Decode(hash Hash, content []byte) error {
	objType, rest, err := entities.DecodeObject(content)
	if err != nil {
		return err
	}
	if objType != string(ObjectTypeSymRef) {
		return fmt.Errorf("cannot decode %s as a symref", objType)
	}

	t.Content = string(rest)
	t.Encoded = content
	t.Hash = hash
	return nil
}

func (t *SymRefObject) Links() []Hash {
	return nil
}

func (t *SymRefObject) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Target string
	}{
		t.Content,
	})
}
