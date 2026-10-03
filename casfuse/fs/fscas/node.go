// Copyright 2025 Google LLC
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

package fscas

import (
	"context"
	"sync/atomic"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/casfuse/fs"
)

type Node interface {
	fs.Node
	IsDirty() bool
	Hash() hash.Hash

	IsLoaded() bool

	IsExcluded() bool
	SetExcluded(bool)

	Snapshot(context.Context) (hash.Hash, int64, error)
	ForceHashAndSize(hash.Hash, int64) error
	InitHash(hash.Hash)

	SetExtType(cas.DirObjectExtType)
	ExtType() cas.DirObjectExtType
}
type node struct {
	hash       hash.Hash
	dirtiness  atomic.Uint32
	isLoaded   bool
	isExcluded bool
	extType    cas.DirObjectExtType
}

func (n *node) initHashAndSize(hash hash.Hash, size uint64) {
}
func (n *node) Updated() {
	n.dirtiness.Add(1)
}
func (n *node) IsDirty() bool {
	return n.dirtiness.Load() > 0
}
func (n *node) MakeDirty() {
	n.dirtiness.Add(1)
}
func (n *node) ClearDirty() {
	n.dirtiness.Store(0)
}
func (n *node) IsExcluded() bool {
	return n.isExcluded
}
func (n *node) IsLoaded() bool {
	return n.isLoaded
}
func (n *node) SetLoaded() {
	n.isLoaded = true
}
func (n *node) Hash() hash.Hash {
	return n.hash
}
func (n *node) InitHash(h hash.Hash) {
	n.hash = h
}
func (n *node) SetExtType(t cas.DirObjectExtType) {
	n.extType = t
}
func (n *node) ExtType() cas.DirObjectExtType {
	return n.extType
}
func NewNode(hash hash.Hash) *node {
	return &node{hash: hash}

}
