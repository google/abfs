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

package casgit

import (
	"bytes"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
)

// casObject implements the plumbing.EncodedObject interface so that it can
// be used to interface with the go-git library
type casObject struct {
	t    plumbing.ObjectType
	hash plumbing.Hash
	buf  []byte
}

func newCASObject(hash plumbing.Hash, t plumbing.ObjectType, buf []byte) *casObject {
	return &casObject{t: t, buf: buf, hash: hash}
}

// Hash returns the object Hash, the hash is calculated on-the-fly the first
// time it's called, in all subsequent calls the same Hash is returned even
// if the type or the content have changed. The Hash is only generated if the
// size of the content is exactly the object size.
func (o *casObject) Hash() plumbing.Hash {
	if o.hash == plumbing.ZeroHash {
		o.hash = plumbing.ComputeHash(o.t, o.buf)
	}

	return o.hash
}

// Type returns the ObjectType
func (o *casObject) Type() plumbing.ObjectType {
	return o.t
}

// SetType sets the ObjectType
func (o *casObject) SetType(t plumbing.ObjectType) {
	o.t = t
}

// Size returns the size of the object
func (o *casObject) Size() int64 {
	return int64(len(o.buf))
}

func (o *casObject) SetSize(s int64) {
	panic("not implemented")
}

// Reader returns an io.ReadCloser used to read the object's content.
//
// For a MemoryObject, this reader is seekable.
func (o *casObject) Reader() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(o.buf)), nil
}

// Writer returns a ObjectWriter used to write the object's content.
func (o *casObject) Writer() (io.WriteCloser, error) {
	panic("not implemented")
}

func (o *casObject) Write(p []byte) (n int, err error) {
	panic("not implemented")
}

// Close releases any resources consumed by the object when it is acting as a
// ObjectWriter.
func (o *casObject) Close() error {
	return nil
}

func EncodedObjectHeader(encoded plumbing.EncodedObject) string {
	return fmt.Sprintf("%s %d\x00", encoded.Type().Bytes(), encoded.Size())
}
