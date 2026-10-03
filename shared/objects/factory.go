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
	"errors"
	"fmt"

	"github.com/google/abfs/shared/entities"
)

type factoryMap map[entities.ObjectType]func() Object

func (f factoryMap) Register(t entities.ObjectType, fn func() Object) {
	f[t] = fn
}

func (f factoryMap) New(t entities.ObjectType) (Object, error) {
	fn, found := f[t]
	if !found {
		return nil, fmt.Errorf("no factory registered for type %q", t)
	}
	return fn(), nil
}

func (f factoryMap) NewEmpty(hash entities.Hash, content []byte) (ObjectType, Object, error) {
	p := bytes.IndexByte(content, ' ')
	if p < 0 {
		return "", nil, errors.New("failed to find space in the object content")
	}
	t := ObjectType(content[0:p])
	fn, found := f[t]
	if !found {
		return t, nil, fmt.Errorf("no factory registered for type %q", t)
	}
	return t, fn(), nil
}

func (f factoryMap) Decode(hash entities.Hash, content []byte) (ObjectType, Object, error) {
	t, obj, err := f.NewEmpty(hash, content)
	if err != nil {
		return "", nil, err
	}
	return t, obj, obj.Decode(hash, content)
}

func (f factoryMap) Links(hash entities.Hash, content []byte) ([]Hash, error) {
	_, obj, err := f.NewEmpty(hash, content)
	if err != nil {
		return nil, err
	}
	if err := obj.Decode(hash, content); err != nil {
		return nil, err
	}
	return obj.Links(), nil
}

var Factory factoryMap = make(factoryMap)
