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
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/utils/ioutil"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const (
	GitDirName = ".git"
)

type GitObject interface {
	objects.Object
	// JsonMarshal will return the JSON encoded bytes of the object. The object must already
	// be decoded, but Encode() does not need to be called first. This exists because directly
	// calling json.Marshal on the go-git types does not provide types friendly for JSON consumption
	// e.g. plumbing.Hash is a byte array.
	MarshalJSON() ([]byte, error)
}

type GitBaseObject struct {
	Hash    Hash
	Encoded []byte
}

func (o *GitBaseObject) Base() *GitBaseObject {
	return o
}

func (o *GitBaseObject) GetHash() Hash {
	return o.Hash
}

func (o *GitBaseObject) GetEncoded() []byte {
	return o.Encoded
}

// We have to wrap the git objects to provide custom functionality.

func objectGitEncode(obj *GitBaseObject, t ObjectType, encode func(plumbing.EncodedObject) error) (returnErr error) {
	buf := new(bytes.Buffer)

	o := &plumbing.MemoryObject{}
	if err := encode(o); err != nil {
		return err
	}
	r, err := o.Reader()
	if err != nil {
		return err
	}
	defer ioutil.CheckClose(r, &returnErr)
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	if err := entities.WriteObject(string(t), raw, buf); err != nil {
		return err
	}
	obj.Encoded = buf.Bytes()
	obj.Hash = sha1.Sum(buf.Bytes())
	return nil
}
