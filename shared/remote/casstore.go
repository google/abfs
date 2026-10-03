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

package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cas/oms/git"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/cas/hash/hashers/sha1"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

var ErrNotSupported = fmt.Errorf("not supported")

// Provide a bindcas.Store with the remote object.
type CASRemoteStore struct {
	r      *RemoteStore
	hasher hash.Hasher
}

func newCASRemoteStore(r *RemoteStore) *CASRemoteStore {
	return &CASRemoteStore{r: r, hasher: sha1.NewHasher()}
}
func (s *CASRemoteStore) Open() error {
	return nil
}
func (s *CASRemoteStore) Close() error {
	return nil
}
func (c *CASRemoteStore) Query(h hash.Hash) (*cas.QueryInfo, error) {
	return nil, ErrNotSupported
}
func (c *CASRemoteStore) OpenFile(h hash.Hash) (io.ReadCloser, error) {
	ctx := context.Background()
	return c.r.OpenBlob(ctx, bindcas.ABFSHash(h))
}
func (c *CASRemoteStore) GetSymLink(h hash.Hash) (out string, retErr error) {
	reader, err := c.OpenFile(h)
	if err != nil {
		return out, err
	}
	defer entities.CloseErrCheck(reader, &retErr)
	raw, err := io.ReadAll(reader)
	if err != nil {
		return out, err
	}
	out = string(raw)
	return out, nil
}
func (c *CASRemoteStore) GetDirList(hash.Hash) (cas.DirListReadCloser, error) {
	return nil, ErrNotSupported
}
func (c *CASRemoteStore) GetDirEntries(h hash.Hash) (cas.DirEntryReadCloser, error) {
	ctx := context.TODO()
	buf, meta, err := c.r.GetTree(ctx, bindcas.ABFSHash(h))
	if err != nil {
		return nil, err
	}
	entries, err := entities.DecodeDir(buf, meta)
	if err != nil {
		return nil, err
	}
	casEntries := make([]cas.DirEntry, len(entries))
	for i, e := range entries {
		casEntries[i] = bindcas.DirEntry(e)
	}
	return cas.NewDirInMemory(casEntries), nil

}
func (c *CASRemoteStore) GetDeepTree(h hash.Hash) (cas.DirEntryReadCloser, error) {
	// Convert to CAS
	return nil, ErrNotSupported
}

func (c *CASRemoteStore) Save(r io.Reader) (hash.Hash, error) {
	// We don't support unknown file size, this is not use in memfs
	// if needed could write r to temp file and then pass to savefile and cleanup
	return nil, ErrNotSupported
}
func (c *CASRemoteStore) SaveFile(filepath string, size int64) (hash hash.Hash, retErr error) {
	hash, err := git.HashFile(c.hasher, filepath, size)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(filepath)
	if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	ctx := context.Background()
	if err := c.r.WriteBlob(ctx, bindcas.ABFSHash(hash), size, f); err != nil {
		return nil, err
	}

	return hash, nil

}
func (c *CASRemoteStore) SaveSymLink(target string) (hash.Hash, error) {
	hash, err := git.HashSymLink(c.hasher, target)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	// Write blob
	if err := c.r.WriteBlob(ctx, bindcas.ABFSHash(hash), int64(len(target)), strings.NewReader(target)); err != nil {
		return nil, err
	}
	return hash, nil
}
func (c *CASRemoteStore) SaveDirList(r cas.DirListReader) (hash.Hash, error) {
	// These are not persisted just encode and hash the data
	return git.EncodeDirList(c.hasher, r)

}
func (c *CASRemoteStore) SaveDirEntries(r cas.DirEntryReader) (hash.Hash, error) {
	// remotestore does not put the tree meta?
	// just send empty for now
	var treeMeta entities.TreeMeta
	hash, content, _, err := git.EncodeDirEntries(c.hasher, r)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if err := c.r.PutTree(ctx, bindcas.ABFSHash(hash), content, treeMeta); err != nil {
		return nil, err
	}
	return hash, nil
}
func (c *CASRemoteStore) SaveDeepTree(r cas.DirEntryReader) (hash.Hash, error) {
	hash, content, err := git.EncodeDeepTree(c.hasher, r)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if err := c.r.PutObject(ctx, bindcas.ABFSHash(hash), content); err != nil {
		return nil, err
	}
	return hash, nil
}
