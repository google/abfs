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
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"

	"github.com/google/abfs/shared/entities"
)

type (
	Hash       = entities.Hash
	ObjectType = entities.ObjectType
)

type JSONBaseObject struct {
	Hash    Hash   `json:"-"`
	Encoded []byte `json:"-"`
}

func (o *JSONBaseObject) Base() *JSONBaseObject {
	return o
}

func (o *JSONBaseObject) GetHash() Hash {
	return o.Hash
}

func (o *JSONBaseObject) GetEncoded() []byte {
	return o.Encoded
}

type Object interface {
	GetHash() Hash
	GetEncoded() []byte
	Encode() error
	Decode(Hash, []byte) error
	Links() []Hash
}

type JSONObject interface {
	Base() *JSONBaseObject
}

func ObjectJSONEncode(o JSONObject, magic ObjectType) error {
	buf := new(bytes.Buffer)

	jsonBytes, err := json.Marshal(o)
	if err != nil {
		return err
	}

	err = entities.WriteObject(string(magic), jsonBytes, buf)
	if err != nil {
		return err
	}

	o.Base().Encoded = buf.Bytes()
	o.Base().Hash = sha1.Sum(buf.Bytes())
	return nil
}

func ObjectJSONDecode(o JSONObject, magic ObjectType, hash Hash, content []byte) error {
	t, buf, err := entities.DecodeObject(content)
	if err != nil {
		return err
	}
	if t != string(magic) {
		return fmt.Errorf("expected a %s object but got %s: %w", magic, t, entities.ErrObjectParse)
	}
	if err := json.Unmarshal(buf, o); err != nil {
		return err
	}
	o.Base().Encoded = content
	o.Base().Hash = hash
	return nil
}
