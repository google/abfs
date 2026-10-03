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

package mem

import (
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/casfuse/fs"
)

var _ fs.Blob = (*Blob)(nil)

func NewBlob() fs.Blob {
	return &Blob{}
}

// Create memory blob with input content
func NewBlobString(content string) fs.Blob {
	out := &Blob{data: make([]byte, len(content))}
	copy(out.data, content)
	return out
}
func NewBlobBytes(d []byte) fs.Blob {
	out := &Blob{data: d}
	return out
}

// Node factory for memory backed directory and files
type nf struct {
	parent fs.FsDir
}

func (n *nf) CreateFileNode(t cas.FileMode) (fs.Node, error) {
	return fs.NewFile(n.parent, t, NewBlob()), nil
}
func (n *nf) CreateDirNode(t cas.FileMode) (fs.Node, error) {
	return fs.NewDir(n.parent, t, n), nil
}
func (n *nf) CreateSymLinkNode(t cas.FileMode) (fs.Node, error) {
	return fs.NewSymLink(n.parent, t), nil
}
func (n *nf) CreateFifoNode(t cas.FileMode) (fs.Node, error) {
	return fs.NewFifo(n.parent, t), nil
}
func (n *nf) CreateSocketNode(t cas.FileMode) (fs.Node, error) {
	return fs.NewSocket(n.parent, t), nil
}

func NewNodeFactory(parent fs.FsDir) *nf {
	return &nf{parent: parent}
}

var _ fs.NodeFactory = (*nf)(nil)
