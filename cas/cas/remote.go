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

package cas

import (
	"io"

	"github.com/google/abfs/cas/hash"
)

// RemoteReader combines a StoreReader and ProviderReader.
// The RemoteReader implements the StoreReader interface when looking for
// data it will follow the following pattern:
//   - Check StoreReader to see if already present
//   - If not query the ProviderReader
//   - If successful in query read the content from the StoreReader
type RemoteReader struct {
	s StoreReader
	p ProviderReader
}

// RemoteWriter combines a StoreWriter and ProviderWriter
// The RemoteWriter implements the StoreWriter interface when adding new
// data it will follow the following pattern:
//   - Store the content to the StoreWriter
//   - If successful notify the ProviderWriter of the new data.
type RemoteWriter struct {
	s StoreWriter
	p ProviderWriter
}

// Provider a full read and write interface to a remote store.
type Remote struct {
	RemoteReader
	RemoteWriter
	s Store
	p Provider
}

// Query find the type associated with the hash
func (r *RemoteReader) Query(h hash.Hash) (*QueryInfo, error) {
	return r.s.Query(h)
}

// OpenFile open a read only file based on hash.
// The file is fetched from the remote if needed.
func (r *RemoteReader) OpenFile(h hash.Hash) (io.ReadCloser, error) {
	if f, err := r.s.OpenFile(h); err == nil {
		return f, err
	}
	if err := r.p.FetchFile(h); err != nil {
		return nil, err
	}
	return r.s.OpenFile(h)
}

// GetSymLink read the symlink target based on hash.
// The symlink is fetched from the remote if needed.
func (r *RemoteReader) GetSymLink(h hash.Hash) (string, error) {
	if s, err := r.s.GetSymLink(h); err == nil {
		return s, err
	}
	if err := r.p.FetchSymLink(h); err != nil {
		return "", err
	}
	return r.s.GetSymLink(h)
}

// GetDirList read the directory list based on the hash
// The entry data is fetched from the remote if needed.
func (r *RemoteReader) GetDirList(h hash.Hash) (DirListReadCloser, error) {
	if de, err := r.s.GetDirList(h); err == nil {
		return de, err
	}
	if err := r.p.FetchDirList(h); err != nil {
		return nil, err
	}
	return r.s.GetDirList(h)
}

// GetDirEntries read the directory entries based on the hash.
// The entry data is fetched from the remote if needed.
func (r *RemoteReader) GetDirEntries(h hash.Hash) (DirEntryReadCloser, error) {
	if de, err := r.s.GetDirEntries(h); err == nil {
		return de, err
	}
	if err := r.p.FetchDirEntries(h); err != nil {
		return nil, err
	}
	return r.s.GetDirEntries(h)
}

// GetDeepTree read the deep tree based on the hash
// The deeptree data is fetched from the remote if needed.
func (r *RemoteReader) GetDeepTree(h hash.Hash) (DirEntryReadCloser, error) {
	if dt, err := r.s.GetDeepTree(h); err == nil {
		return dt, err
	}
	if err := r.p.FetchDeepTree(h); err != nil {
		return nil, err
	}
	return r.s.GetDeepTree(h)
}

// Save given a reader write the data to the store and notify remote
func (r *RemoteWriter) Save(ior io.Reader) (hash.Hash, error) {
	hash, err := r.s.Save(ior)
	if err != nil {
		return nil, err
	}
	r.p.NewFile(hash)
	return hash, nil
}

// SaveFile given the input file path and size commit the file.
// Save the file to the store and notify the remote end point of the new hash.
func (r *RemoteWriter) SaveFile(filepath string, size int64) (hash.Hash, error) {
	hash, err := r.s.SaveFile(filepath, size)
	if err != nil {
		return nil, err
	}
	r.p.NewFile(hash)
	return hash, nil
}

// SaveSymLink given the symlink target commit the symlink.
// Save the symlink to the store and notify the remote end point of the new hash.
func (r *RemoteWriter) SaveSymLink(target string) (hash.Hash, error) {
	h, err := r.s.SaveSymLink(target)
	if err != nil {
		return nil, err
	}
	r.p.NewSymLink(h)
	return h, err
}

// SaveDirEntries given a collection of directory entries commit the data.
// Save the directory entries to the store and notify remote end point of
// the new hash.
func (r *RemoteWriter) SaveDirEntries(de DirEntryReader) (hash.Hash, error) {
	h, err := r.s.SaveDirEntries(de)
	if err != nil {
		return nil, err
	}
	r.p.NewDirEntries(h)
	return h, nil
}

// SaveDirList given a collection of directory list entries commit the data.
// Save the directory list to the store and notify remote end point of
// the new hash.
func (r *RemoteWriter) SaveDirList(dl DirListReader) (hash.Hash, error) {
	h, err := r.s.SaveDirList(dl)
	if err != nil {
		return nil, err
	}
	r.p.NewDirList(h)
	return h, nil
}

// SaveDeepTree given a deep tree commit the data.
// Save the deep tree to the store and notify remote end point of the
// new hash.
func (r *RemoteWriter) SaveDeepTree(dt DirEntryReader) (hash.Hash, error) {
	h, err := r.s.SaveDeepTree(dt)
	if err != nil {
		return nil, err
	}
	r.p.NewDeepTree(h)
	return h, nil
}

func (r *Remote) Open() error {
	if err := r.s.Open(); err != nil {
		return err
	}
	return r.p.Open()
}
func (r *Remote) Close() error {
	if err := r.p.Close(); err != nil {
		return err
	}
	return r.s.Close()

}

func (r *Remote) GetBase() (Store, Provider) {
	return r.s, r.p
}

var _ StoreReader = (*RemoteReader)(nil)
var _ StoreWriter = (*RemoteWriter)(nil)
var _ Store = (*Remote)(nil)

// NewRemoteReader combine a store reader and provider reader to create a RemoteReader
func NewRemoteReader(s StoreReader, p ProviderReader) *RemoteReader {
	return &RemoteReader{s: s, p: p}
}

// NewRemoteWriter combine a store writer and provider writer to create a RemoteWriter
func NewRemoteWriter(s StoreWriter, p ProviderWriter) *RemoteWriter {
	return &RemoteWriter{s: s, p: p}
}

// NewRemote combines a store and provider to create a Remote
func NewRemote(s Store, p Provider) *Remote {
	return &Remote{
		RemoteReader: RemoteReader{s: s, p: p},
		RemoteWriter: RemoteWriter{s: s, p: p},
		s:            s,
		p:            p,
	}
}
