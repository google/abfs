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

package localcache

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"syscall"

	"github.com/pkg/xattr"
	"golang.org/x/sys/unix"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const (
	BlobPrefix   = "blobs/"
	ObjectPrefix = "objects/"
	MapPrefix    = "map/"
)

type CompressMode bool

const (
	Compressed   CompressMode = true
	Uncompressed CompressMode = false
)

type LocalStore struct {
	dir          string
	levels       int
	notify       Notify
	compressMode CompressMode
	fsync        bool
}

type Notify struct {
	PutFile NotifyFunc
	Write   NotifyFunc
	Read    NotifyFunc
}

type LocalStoreOpts struct {
	Dir          string
	Levels       int
	Notify       Notify
	CompressMode CompressMode
	Fsync        bool
}

func NewLocalStore(opts LocalStoreOpts) (*LocalStore, error) {

	if err := os.MkdirAll(opts.Dir, cacheDirsUmask); err != nil {
		return nil, err
	}
	if opts.Levels != 2 && opts.Levels != 3 {
		return nil, fmt.Errorf("number of levels not supported: %d", opts.Levels)
	}
	return &LocalStore{
		dir:          opts.Dir,
		levels:       opts.Levels,
		notify:       opts.Notify,
		compressMode: opts.CompressMode,
		fsync:        opts.Fsync,
	}, nil
}

// Path returns the local path from where the file can be read.
func (c *LocalStore) Path(id entities.Hash) string {
	return c.PathHex(id.String())
}

// PathHex returns the sharded on-disk filesystem path for a hex-encoded key
// (supporting arbitrary lengths, such as a 40-character SHA-1 or 64-character SHA-256).
//
// Sharding divides cached files across subdirectories to prevent large directory scaling
// bottlenecks (e.g., filesystem lookup overhead with hundreds of thousands of files in a single directory).
//
// Examples for levels = 2:
//   - SHA-1:   <dir>/e69/de29bb2d1d6434b8b29ae775ad8c2e48c5391
//   - SHA-256: <dir>/e3b/0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
func (c *LocalStore) PathHex(str string) string {
	switch c.levels {
	case 2:
		// 2-level hierarchy: <dir>/<first 3 hex chars>/<remaining hex chars>
		return fmt.Sprintf("%s/%s/%s", c.dir, str[:3], str[3:])
	case 3:
		// 3-level hierarchy: <dir>/<first 3 hex chars>/<next 3 hex chars>/<remaining hex chars>
		return fmt.Sprintf("%s/%s/%s/%s", c.dir, str[:3], str[3:6], str[6:])
	default:
		panic("unexpected levels value")
	}
}

// HasHex checks if a file exists on disk for the given hex-encoded key.
func (c *LocalStore) HasHex(hexStr string) bool {
	p := c.PathHex(hexStr)
	_, err := os.Stat(p)
	return err == nil
}

func (c *LocalStore) PathMeta(id entities.Hash, meta string) string {
	return c.Path(id) + "." + meta
}

// OpenFile returns a file corresponding to the blob, opened for reading.
func (c *LocalStore) OpenFile(ctx context.Context, id entities.Hash) (io.ReadCloser, error) {
	p := c.Path(id)
	f, err := os.Open(p)
	if err := c.NotifyRead(ctx, id); err != nil {
		return nil, err
	}
	return f, err
}

func (c *LocalStore) Size(ctx context.Context, key entities.Hash) (int64, error) {
	p := c.Path(key)
	stat, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	if err := c.NotifyRead(ctx, key); err != nil {
		return 0, err
	}
	return stat.Size(), nil
}

func (c *LocalStore) Stat(ctx context.Context, key entities.Hash) (entities.StatInfo, error) {
	var ret entities.StatInfo
	p := c.Path(key)
	stat, err := os.Lstat(p)
	if err != nil {
		return ret, err
	}
	if err := c.NotifyRead(ctx, key); err != nil {
		return ret, err
	}
	ret.Size = stat.Size()
	ret.Mode = entities.FileMode(stat.Mode())
	return ret, nil
}

func (c *LocalStore) ObjectHeader(ctx context.Context, key entities.Hash) (hdr entities.ObjectHeader, retErr error) {
	f, err := c.OpenFile(ctx, key)
	if err != nil {
		return entities.ObjectHeader{}, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	r, err := c.readCloser(f)
	if err != nil {
		return entities.ObjectHeader{}, err
	}
	defer entities.CloseErrCheck(r, &retErr)
	stat, err := os.Stat(c.Path(key))
	if err != nil {
		return entities.ObjectHeader{}, err
	}

	t, s, err := objects.ReadHeader(r)

	return entities.ObjectHeader{
		Type:           entities.ObjectType(t),
		Size:           s,
		CompressedSize: stat.Size(),
	}, err
}

func (c *LocalStore) GetXAttr(ctx context.Context, key entities.Hash, attr string) (string, error) {
	p := c.Path(key)
	buf, err := xattr.LGet(p, attr)
	if err := c.NotifyRead(ctx, key); err != nil {
		return "", err
	}
	return string(buf), err
}

func (c *LocalStore) writeLocalFile(path string, r io.Reader, skipCompression bool, beforeRename func(string) error) (sz int64, err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, cacheDirsUmask); err != nil {
		return sz, err
	}
	f, err := os.CreateTemp(dir, "tmp")
	if err != nil {
		return sz, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()

	writeCloser := c.writeCloser(f, skipCompression)
	sz, err = io.Copy(writeCloser, r)
	if err != nil {
		if err := writeCloser.Close(); err != nil {
			log.Printf("Error closing writeCloser: %v", err)
		}
		return sz, err
	}
	if err := writeCloser.Close(); err != nil {
		return sz, err
	}

	if c.fsync {
		// Ensure the contents of the file are flushed to disk
		// The directory metadata is flushed later
		if err := f.Sync(); err != nil {
			return sz, err
		}
	}

	closed = true
	if err := f.Close(); err != nil {
		return sz, err
	}

	if beforeRename != nil {
		if err := beforeRename(f.Name()); err != nil {
			return sz, err
		}
	}

	err = renameNoReplace(f.Name(), path, c.fsync)
	if errors.Is(err, os.ErrExist) {
		_ = os.Remove(f.Name())
		err = nil
	}

	return sz, err
}

func renameNoReplace(oldpath, newpath string, fsync bool) (retErr error) {
	err := unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
	if err != nil {
		return err
	}
	if !fsync {
		return nil
	}

	// Ensure dir metadata is flushed to disk
	dir, err := os.Open(path.Dir(newpath))
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(dir, &retErr)

	return dir.Sync()
}

// Write uses the provided reader to write the file contents into
// the cache at the provided hash. `beforeRename` allows callers to
// modify the file before it is renamed to its final location.
func (c *LocalStore) Write(ctx context.Context, id entities.Hash, r io.Reader, beforeRename func(string) error) error {
	p := c.Path(id)
	if _, err := os.Stat(p); err == nil {
		// if key already exists, ignore write request because content should match
		return nil
	}
	sz, err := c.writeLocalFile(p, r, false, beforeRename)
	if err := c.NotifyWrite(ctx, id); err != nil {
		return err
	}
	if err == nil && sz == 0 && id != entities.EmptyFileHash {
		log.Printf("Wrote hash %s with zero size but not empty hash\n", id)
	}
	return err
}

// WriteHex writes content at the given hex path (e.g. SHA-256 CAS blob).
func (c *LocalStore) WriteHex(ctx context.Context, hexStr string, r io.Reader, beforeRename func(string) error) error {
	p := c.PathHex(hexStr)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	_, err := c.writeLocalFile(p, r, false, beforeRename)
	return err
}

func ignoreExistsError(err error) error {
	if err == syscall.EEXIST {
		return nil
	}
	return err
}

type readerNOPCloser struct {
	r io.Reader
}

func (r *readerNOPCloser) Read(buf []byte) (int, error) {
	return r.r.Read(buf)
}

func (r *readerNOPCloser) Close() error {
	return nil
}

func (c *LocalStore) readCloser(r io.Reader) (io.ReadCloser, error) {
	if c.compressMode == Uncompressed {
		return &readerNOPCloser{r}, nil
	}

	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, err
	}

	return zr, nil
}

type writerNOPCloser struct {
	w io.Writer
}

func (w *writerNOPCloser) Write(buf []byte) (int, error) {
	return w.w.Write(buf)
}

func (w *writerNOPCloser) Close() error {
	return nil
}

// The returned writeCloser must be closed before 'w' is closed to ensure
// internal buffering is flushed.
func (c *LocalStore) writeCloser(w io.Writer, skipCompression bool) io.WriteCloser {
	if c.compressMode == Uncompressed || skipCompression {
		return &writerNOPCloser{w}
	}

	return zlib.NewWriter(w)
}

func (c *LocalStore) PutFile(ctx context.Context, key entities.Hash, filename string) error {
	p := c.Path(key)
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, cacheDirsUmask); err != nil {
		return err
	}
	err := syscall.Rename(filename, p)
	if err = ignoreExistsError(err); err == nil && c.notify.PutFile != nil {
		return c.notify.PutFile(ctx, key)
	}
	return err
}

func (c *LocalStore) Get(ctx context.Context, key entities.Hash) (content []byte, retErr error) {
	p := c.Path(key)
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	if err := c.NotifyRead(ctx, key); err != nil {
		return nil, err
	}

	r, err := c.readCloser(f)
	if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(r, &retErr)

	return io.ReadAll(r)
}

func (c *LocalStore) GetMeta(ctx context.Context, key entities.Hash, meta string) (content []byte, retErr error) {
	p := c.PathMeta(key, meta)
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	if err := c.NotifyRead(ctx, key); err != nil {
		return nil, err
	}

	// We aren't zlib compressing meta.
	return io.ReadAll(f)
}

func (c *LocalStore) Put(ctx context.Context, key entities.Hash, content []byte) error {
	buf := bytes.NewBuffer(content)
	return c.Write(ctx, key, buf, nil)
}

func (c *LocalStore) PutMeta(ctx context.Context, key entities.Hash, meta string, content []byte) error {
	p := c.PathMeta(key, meta)
	buf := bytes.NewBuffer(content)
	_, err := c.writeLocalFile(p, buf, true, nil)
	if err := c.NotifyWrite(ctx, key); err != nil {
		return err
	}

	return err
}

func (c *LocalStore) MetaSize(ctx context.Context, key entities.Hash, meta string) (int64, error) {
	p := c.PathMeta(key, meta)
	stat, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	if err := c.NotifyRead(ctx, key); err != nil {
		return 0, err
	}
	return stat.Size(), nil
}

func (c *LocalStore) RemoveMeta(key entities.Hash, meta string) error {
	p := c.PathMeta(key, meta)
	return os.Remove(p)
}

func (c *LocalStore) Remove(key entities.Hash) error {
	p := c.Path(key)
	return os.Remove(p)
}

func (c *LocalStore) Has(key entities.Hash) (bool, error) {
	_, err := os.Stat(c.Path(key))
	if err == nil {
		return true, nil
	}
	if entities.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (c *LocalStore) NotifyRead(ctx context.Context, key entities.Hash) error {
	if c.notify.Read == nil {
		return nil
	}

	return c.notify.Read(context.Background(), key)
}

func (c *LocalStore) NotifyWrite(ctx context.Context, key entities.Hash) error {
	if c.notify.Write == nil {
		return nil
	}

	return c.notify.Write(context.Background(), key)
}
