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

package git

import (
	"errors"
	"fmt"
	"io"
	"path"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
)

var (
	ErrNotSupported = errors.New("not supported")
)

type CASStore struct {
	RootPath string
	Scratch  *Scratch
	Blobs    *Blobs
	Objects  *Objects
}

const (
	blobsDir   = "blobs"
	objectsDir = "objects"
	scratchDir = "scratch"
)

type hloc struct{}

func (hloc) Path(h hash.Hash) string {
	str := h.String()
	return fmt.Sprintf("%s/%s", str[:3], str[3:])

}

// NewCASStore create GIT object model store.  The hasher to used is passed in
// but it should be a SHA-1 to match the libraries used.
func NewCASStore(root, scratch string, hasher hash.Hasher, exclusiveScratch bool) *CASStore {
	// scratch can be empty so don't form path until Open call
	out := &CASStore{
		RootPath: root,
		Scratch:  NewScratch(scratch, exclusiveScratch),
		Blobs:    NewBlobs(path.Join(root, blobsDir), hloc{}, hasher),
		Objects:  NewObjects(path.Join(root, objectsDir), hloc{}, hasher),
	}

	return out

}

// Open create the blob and object stores as needed
func (c *CASStore) Open() error {

	if err := c.Scratch.Init(path.Join(c.RootPath, scratchDir)); err != nil {
		return err
	}
	if err := c.Blobs.Init(c.Scratch); err != nil {
		return err
	}
	if err := c.Objects.Init(c.Scratch); err != nil {
		return err
	}
	return nil
}
func (c *CASStore) Close() error {
	// Clean up scratch
	return c.Scratch.Cleanup()
}
func (c *CASStore) Save(r io.Reader) (hash.Hash, error) {
	return c.Blobs.Save(r)
}
func (c *CASStore) SaveFile(filepath string, size int64) (hash.Hash, error) {
	return c.Blobs.SaveFile(filepath, size)
}
func (c *CASStore) SaveSymLink(target string) (hash.Hash, error) {
	return c.Blobs.SaveSymLink(target)
}
func (c *CASStore) SaveDirEntries(reader cas.DirEntryReader) (hash.Hash, error) {
	return c.Objects.SaveDirEntries(reader)
}
func (c *CASStore) SaveDirList(r cas.DirListReader) (hash.Hash, error) {
	return c.Objects.SaveDirList(r)
}

func (c *CASStore) SaveDeepTree(reader cas.DirEntryReader) (hash.Hash, error) {
	return c.Objects.SaveDeepTree(reader)
}

func (c *CASStore) OpenFile(h hash.Hash) (io.ReadCloser, error) {
	return c.Blobs.OpenFile(h)
}
func (c *CASStore) Query(h hash.Hash) (*cas.QueryInfo, error) {
	if item, err := c.Blobs.Query(h); err == nil {
		return item, err
	}
	return c.Objects.Query(h)
}
func (c *CASStore) GetSymLink(h hash.Hash) (string, error) {
	return c.Blobs.GetSymLink(h)
}

func (c *CASStore) GetDirList(h hash.Hash) (cas.DirListReadCloser, error) {
	return nil, ErrNotSupported
}
func (c *CASStore) GetDirEntries(h hash.Hash) (cas.DirEntryReadCloser, error) {
	return c.Objects.GetDirEntries(h)
}
func (c *CASStore) GetDeepTree(h hash.Hash) (cas.DirEntryReadCloser, error) {
	return c.Objects.GetDeepTree(h)
}
