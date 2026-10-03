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
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/google/abfs/cas/hash"
	"golang.org/x/sys/unix"
)

const (
	cacheDirsUmask = 0700
)

// HashToFileLoc given a hash convert to where file is stored locally
type HashToFileLoc interface {
	Path(h hash.Hash) string
}

// Opening files from the store requires the file and a closer
// the closer can be a noop closer or the zlib closer.
// Clients need to close the file prior to the closer to keep
// this simple will return this object that contains a single
// Close() the clients can use
type openFile struct {
	f *os.File
	c io.ReadCloser
}

// clients should read from closer
func (o *openFile) Reader() io.Reader {
	return o.c
}

// follow correct close order
func (o *openFile) Close() error {
	e1 := o.f.Close()
	e2 := o.c.Close()
	return errors.Join(e1, e2)
}
func (o *openFile) Read(d []byte) (int, error) {
	return o.Reader().Read(d)
}

type Store struct {
	Dir     string        // Root directory for the store
	Hloc    HashToFileLoc // Hash to file location
	Hasher  hash.Hasher   // Hasher
	Scratch *Scratch
}

func newStore(dir string, hloc HashToFileLoc, hasher hash.Hasher) Store {
	return Store{Dir: dir,
		Hloc:   hloc,
		Hasher: hasher}

}
func (s *Store) Init(scratch *Scratch) error {
	if err := os.MkdirAll(s.Dir, cacheDirsUmask); err != nil {
		return err
	}
	s.Scratch = scratch
	return nil
}

// Path get the storage path of the hash within the store
func (s *Store) Path(h hash.Hash) (string, os.FileInfo, error) {
	hashPath := s.Hloc.Path(h)
	loc := path.Join(s.Dir, hashPath)
	fi, err := os.Stat(loc)
	return loc, fi, err
}

// Path get the storage path of the hash meta within the store
func (s *Store) PathMeta(h hash.Hash, tag string) (string, os.FileInfo, error) {
	hashPath := s.Hloc.Path(h)
	loc := path.Join(s.Dir, hashPath)
	loc += fmt.Sprintf(".%s", tag)
	fi, err := os.Stat(loc)
	return loc, fi, err
}

// Open a file based on the input hash and mode
func (s *Store) Open(h hash.Hash, mode compressMode) (*openFile, error) {
	loc, _, err := s.Path(h)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(loc)
	if err != nil {
		return nil, err
	}
	r, err := readCloser(f, mode)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &openFile{f: f, c: r}, nil
}

// Open a file based on the input hash and mode
func (s *Store) OpenMeta(h hash.Hash, tag string, mode compressMode) (*openFile, error) {
	loc, _, err := s.PathMeta(h, tag)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(loc)
	if err != nil {
		return nil, err
	}
	r, err := readCloser(f, mode)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &openFile{f: f, c: r}, nil
}

// ReadAll open and read all data associated with an hash in this store
func (s *Store) ReadAll(h hash.Hash, mode compressMode) ([]byte, error) {
	o, err := s.Open(h, mode)
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return io.ReadAll(o.Reader())

}

// Write store the content in the io.Reader corresponding to the input hash
// If the hash data does not already write the data
func (s *Store) Write(h hash.Hash, r io.Reader, m compressMode) error {
	loc, _, err := s.Path(h)
	return s.writeIfNeeded(err, loc, r, m)
}

func (s *Store) WriteMeta(h hash.Hash, tag string, r io.Reader, m compressMode) error {
	loc, _, err := s.PathMeta(h, tag)
	return s.writeIfNeeded(err, loc, r, m)
}

func (s *Store) ReadMeta(h hash.Hash, tag string, m compressMode) ([]byte, error) {
	o, err := s.OpenMeta(h, tag, m)
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return io.ReadAll(o.Reader())
}

// Files are not updated once written, check the error code from
// a stat call to determine if a file should be written or not.
func (s *Store) writeIfNeeded(statErr error, path string, r io.Reader, mode compressMode) error {
	if statErr == nil {
		// File exists
		return nil
	}
	// Error stating the file
	// If not exist write the file otherwise return the stat error
	if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	tmpFile, _, err := s.writeScratchFile(r, mode)
	if err != nil {
		return err
	}
	return s.commitFile(tmpFile, path)
}

// writeScratchFile write the input reader data to temporary file within the store
func (s *Store) writeScratchFile(r io.Reader, mode compressMode) (string, int64, error) {

	name, fd, err := s.CreateScratchFile()
	if err != nil {
		return "", 0, err
	}
	f := os.NewFile(uintptr(fd), name)
	closed := false
	defer func() {
		if !closed {
			f.Close()
		}
		if err != nil {
			s.Scratch.Remove(name)
		}
	}()

	writeCloser := writeCloser(f, mode)
	sz, err := io.Copy(writeCloser, r)
	if err != nil {
		writeCloser.Close()
		return "", 0, err
	}
	if err := writeCloser.Close(); err != nil {
		return "", 0, err
	}

	closed = true

	if err := f.Close(); err != nil {
		return "", 0, err
	}
	return name, sz, err

}

func (s *Store) commitFile(srcFile, path string) error {

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, cacheDirsUmask); err != nil {
		return err
	}
	err := renameNoReplace(srcFile, path)
	if errors.Is(err, os.ErrExist) {
		s.Scratch.Remove(srcFile)
		err = nil
	}

	return err

}

func (s *Store) CreateScratchFile() (string, int, error) {
	return s.Scratch.Create()
}
func (s *Store) CreateScratchFileInum(inum int) (string, int, error) {
	return s.Scratch.CreateInum(inum)
}
func (s *Store) ScratchRemove(f string) error {
	return s.Scratch.Remove(f)
}

func renameNoReplace(oldpath, newpath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
}
