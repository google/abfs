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

package bindcas

import (
	"io"
	"os"
	"path/filepath"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cas/oms/git"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/cas/hash/hashers"
	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
	"github.com/google/abfs/casfuse/fs/fsapi"
	"github.com/google/abfs/casfuse/fs/fscas"
	"github.com/google/abfs/casfuse/fs/fscas/tap"

	"github.com/google/abfs/shared/entities"
)

type MemFS interface {
	Remove(caspath string) error
	// Allow for files to be imported/exported
	FileExists(caspath string) bool
	ExportFile(caspath string, out io.Writer) error
	ImportFile(in io.Reader, caspath string) error
	// Hash root directory
	Hash() (entities.Hash, error)
}

type memfs struct {
	casfs CASFS
}

// storeWriter this is supported when using abfs.Store
// currently not supporting abfs.Remote
type storeWriter struct {
	blobs    *git.Blobs
	provider cas.Provider
}

func (n *storeWriter) FilePath(h hash.Hash) (string, os.FileInfo, error) {
	if n.blobs == nil {
		return "", nil, casfuse.ErrNotSupported
	}
	path, info, err := n.blobs.Path(h)
	if err == nil || n.provider == nil {
		return path, info, err
	}
	if err := n.provider.FetchFile(h); err != nil {
		return "", nil, err
	}
	return n.blobs.Path(h)

}
func (n *storeWriter) CreateStagingFile() (string, int, error) {
	if n.blobs == nil {
		return "", 0, casfuse.ErrNotSupported
	}
	return n.blobs.CreateScratchFile()
}
func (n *storeWriter) RemoveStagingFile(f string) error {
	if n.blobs == nil {
		return casfuse.ErrNotSupported
	}
	return n.blobs.ScratchRemove(f)
}

func (m *memfs) Remove(caspath string) error {
	return m.casfs.Remove(caspath)
}
func (m *memfs) FileExists(caspath string) bool {
	if _, err := m.casfs.Stat(caspath); err == nil {
		return true
	}
	return false
}
func (m *memfs) ExportFile(caspath string, out io.Writer) (retErr error) {
	fi, err := m.casfs.Stat(caspath)
	if err != nil {
		return err
	}
	f, err := m.casfs.OpenFile(caspath, os.O_RDONLY, entities.FileModeRegular)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(f, &retErr)
	sf := io.NewSectionReader(f, 0, fi.Size())
	_, err = io.Copy(out, sf)
	return err
}
func (m *memfs) ImportFile(in io.Reader, caspath string) (retErr error) {
	dir := filepath.Dir(caspath)
	if err := m.casfs.MkdirAll(dir, entities.FileModeDir); err != nil {
		return err
	}
	f, err := m.casfs.OpenFile(caspath, os.O_CREATE|os.O_TRUNC, entities.FileModeRegular)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(f, &retErr)
	writer := &writer{w: f, off: 0}
	_, err = io.Copy(writer, in)
	return err
}
func (m *memfs) Hash() (entities.Hash, error) {
	return m.casfs.Hash(".")
}
func createInMemFS(s ABFSCASStore) CASFS {
	store := s.CAS()

	// Create store writer based on underlying CAS store.
	var blobs *git.Blobs
	var provider cas.Provider
	if r, ok := store.(*cas.Remote); ok {
		basestore, p := r.GetBase()
		if os, ok := basestore.(*git.CASStore); ok {
			blobs = os.Blobs
		}
		provider = p
	} else {
		if os, ok := store.(*git.CASStore); ok {
			blobs = os.Blobs
		}
	}

	sw := &storeWriter{blobs: blobs, provider: provider}

	hasher := hashers.CreateHasher(hashers.HasherTypeSHA1)
	// Create FS we don't care about ids since it is not export to
	// actual filesystem
	fs := fs.NewFS(0, 0, 0)
	// Drive this out
	tap := tap.NoTap{}
	fs.RegisterExt(casfuse.CASTap, tap)
	fscas.RegisterCAS(fs, hasher, store, sw, tap)
	fscas.NewRootDir(fs)
	//Create in-memory cas fs object with input directory hash
	casfsapi := casfuse.NewCASFSAPI(".", fsapi.NewCASAPI(fs), fs)
	fsapi := casfuse.NewFSAPI(fs)
	return NewCASFS(casfsapi, fsapi)
}

// Proivde CAS like functionality within memory and allow for files to be
// exported and imported from a normal filesystem.  This allows for CASFS
// like operations without having to do a full filesystem mount.
func NewMemFS(s ABFSCASStore, hash entities.Hash) (MemFS, error) {
	c := createInMemFS(s)
	if err := c.SetHash(".", hash); err != nil {
		return nil, err
	}
	return &memfs{casfs: c}, nil
}

type writer struct {
	w   io.WriterAt
	off int64
}

func (w *writer) Write(p []byte) (n int, err error) {
	n, err = w.w.WriteAt(p, w.off)
	w.off += int64(n)
	return n, err
}
