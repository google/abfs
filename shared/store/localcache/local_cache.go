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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"

	"github.com/gofrs/flock"

	"github.com/google/abfs/shared/entities"
)

const (
	CacheVersion   = "0.0.5"
	CacheLockFile  = "cache.lock"
	VersionFile    = "version"
	cacheDirsUmask = 0700
	TreeMetaTag    = "tree"
)

var ErrHashMismatch = errors.New("content hash did not match provided hash")
var ErrIncompatibleExisting = errors.New("existing cache is not compatible with current version")

type RefStore interface {
	Put(server string, project string, refs []entities.RefHash) error
	Get(server string, project string, refs []string) ([]entities.Hash, error)
	Search(server string, project string, refPrefix string) ([]entities.ProjectRefHash, error)
	Close() error
}

type LocalCache struct {
	dir         string
	compressor  Compressor
	blobStore   *LocalStore
	objectStore *LocalStore
	ariStore    *LocalStore
	mapStore    *MapStore
	refStore    RefStore
	cacheLock   *flock.Flock
}

type NotifyFunc func(ctx context.Context, key entities.Hash) error
type CacheOpt interface{}
type notifyPutBlobOpt struct {
	Func NotifyFunc
}
type notifyWriteOpt struct {
	Func NotifyFunc
}
type notifyReadOpt struct {
	Func NotifyFunc
}
type lockOpt struct {
	exclusive bool
}

func NotifyPutBlobOpt(f NotifyFunc) CacheOpt {
	return notifyPutBlobOpt{f}
}

func NotifyWriteOpt(f NotifyFunc) CacheOpt {
	return notifyWriteOpt{f}
}

func NotifyReadOpt(f NotifyFunc) CacheOpt {
	return notifyReadOpt{f}
}

func ExclusiveLockOpt() CacheOpt {
	return lockOpt{true}
}

type plainEmptyObjectsOpt struct {
	enable bool
}

func PlainEmptyObjectsOpt() CacheOpt {
	return plainEmptyObjectsOpt{true}
}

type logRefStoreOpt struct {
	enable bool
}

func LogRefStoreOpt() CacheOpt {
	return logRefStoreOpt{true}
}

type mapStoreOpt struct {
	enable bool
}

func MapStoreOpt() CacheOpt {
	return mapStoreOpt{true}
}

type createIfNotExistOpt struct {
	create bool
}

func CreateIfNotExistOpt() CacheOpt {
	return createIfNotExistOpt{true}
}

type bypassCompatCheckOpt struct {
	bypass bool
}

func BypassCompatCheckOpt() CacheOpt {
	return bypassCompatCheckOpt{true}
}

type fsyncOpt struct {
	fsync bool
}

func FsyncOpt(fsync bool) CacheOpt {
	return fsyncOpt{fsync}
}

type noObjectCompressionOpt struct{}

func NoObjectCompressionOpt() CacheOpt {
	return noObjectCompressionOpt{}
}

type Compressor interface {
	Compress([]byte) ([]byte, error)
	Decompress([]byte) ([]byte, error)
	NewReader(r io.Reader) io.ReadCloser
}

type compressOpt struct {
	compressor Compressor
}

// CompressOpt also stores blob sizes in an xattr since the size can't
// be obtained from stat
func CompressOpt(c Compressor) CacheOpt {
	return compressOpt{c}
}

type validateGitHashOpt struct{}

func ValidateGitHashOpt() CacheOpt {
	return validateGitHashOpt{}
}

type levelsOpt struct {
	levels int
}

func LevelsOpt(levels int) CacheOpt {
	return levelsOpt{levels}
}

func WriteCacheVersion(dir, ver string) error {
	return os.WriteFile(path.Join(dir, VersionFile), []byte(ver), 0644)
}
func WriteCacheVersionIfNotPresent(dir string) error {
	_, err := os.ReadFile(path.Join(dir, VersionFile))
	if entities.IsNotExist(err) {
		return WriteCacheVersion(dir, CacheVersion)
	}
	return err
}

func NewCache(dir string, opts ...CacheOpt) (*LocalCache, error) {
	blobNotify := Notify{}
	objNotify := Notify{}

	withLogRefStore := false
	withMapStore := false
	withPlainEmptyObjectsOpt := false
	create := false
	var compressor Compressor
	levels := 2
	exclLock := false
	bypassCompatCheck := false
	fsync := false

	objectCompressMode := Compressed

	for _, opt := range opts {
		switch o := opt.(type) {
		case notifyPutBlobOpt:
			blobNotify.PutFile = o.Func
		case notifyWriteOpt:
			blobNotify.Write = o.Func
			objNotify.Write = o.Func
		case notifyReadOpt:
			blobNotify.Read = o.Func
			objNotify.Read = o.Func
		case plainEmptyObjectsOpt:
			withPlainEmptyObjectsOpt = o.enable
		case lockOpt:
			exclLock = o.exclusive
		case logRefStoreOpt:
			withLogRefStore = o.enable
		case mapStoreOpt:
			withMapStore = o.enable
		case createIfNotExistOpt:
			create = o.create
		case compressOpt:
			compressor = o.compressor
		case levelsOpt:
			levels = o.levels
		case bypassCompatCheckOpt:
			bypassCompatCheck = o.bypass
		case fsyncOpt:
			fsync = o.fsync
		case noObjectCompressionOpt:
			objectCompressMode = Uncompressed
		default:
			return nil, fmt.Errorf("unsupported opts %T", opt)
		}
	}
	if !create {
		if _, err := os.Stat(path.Join(dir, "tmp")); err != nil {
			if entities.IsNotExist(err) {
				log.Printf("ABFS cache directory doesn't exist consider using `abfs init` to set it up!")
			}
			return nil, err
		}
	} else {
		if err := os.MkdirAll(path.Join(dir, "tmp"), cacheDirsUmask); err != nil {
			return nil, err
		}
	}
	if blobNotify.PutFile != nil && compressor != nil {
		return nil, fmt.Errorf("LocalCache currently doesn't support the notifyPutBlob and compressor options to be used together")
		// we would need to write the blob size xattr when notifyPutBlob is called, but this isn't a use case
		// in the system right now
	}

	blobStore, err := NewLocalStore(LocalStoreOpts{Dir: path.Join(dir, "blobs"), Levels: levels, Notify: blobNotify, CompressMode: Uncompressed, Fsync: fsync})
	if err != nil {
		return nil, err
	}

	// If we have a cache-level compressor already (e.g. on server), we need to
	// disable zlib compression at the store level.
	if compressor != nil {
		objectCompressMode = Uncompressed
	}
	objectStore, err := NewLocalStore(LocalStoreOpts{Dir: path.Join(dir, "objects"), Levels: levels, Notify: objNotify, CompressMode: objectCompressMode, Fsync: fsync})
	if err != nil {
		return nil, err
	}
	ariStore, err := NewLocalStore(LocalStoreOpts{Dir: path.Join(dir, "aris"), Levels: levels, Notify: blobNotify, CompressMode: Uncompressed, Fsync: fsync})
	if err != nil {
		return nil, err
	}

	lock := flock.New(path.Join(dir, CacheLockFile))
	c := &LocalCache{
		dir:         dir,
		compressor:  compressor,
		blobStore:   blobStore,
		objectStore: objectStore,
		ariStore:    ariStore,
		cacheLock:   lock,
	}

	if !bypassCompatCheck {
		compat := c.CompatibleExistingCache()
		if !compat {
			return nil, fmt.Errorf("%w: please restart cacheman to fix", ErrIncompatibleExisting)
		}
	}

	if withMapStore {
		mapStore, err := NewMapStore(path.Join(dir, "map"), levels)
		if err != nil {
			return nil, err
		}
		c.mapStore = mapStore
	}

	if withLogRefStore {
		s, err := NewLogRefStore(path.Join(dir, "refs"))
		if err != nil {
			return nil, err
		}
		c.refStore = s
	}

	if withPlainEmptyObjectsOpt {
		if err := c.WriteBlob(context.Background(), entities.EmptyFileHash, bytes.NewBuffer(nil)); err != nil {
			return nil, err
		}

		//TODO make it work with compressed
		if err := c.objectStore.Put(context.Background(), entities.EmptyDirHash, entities.EmptyDirContent); err != nil {
			return nil, err
		}

		if err := c.objectStore.PutMeta(context.Background(), entities.EmptyDirHash, TreeMetaTag, nil); err != nil {
			return nil, err
		}
	}

	if exclLock {
		ok, err := lock.TryLock()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("could not acquire exclusive lock - try to unmount any active abfs mounts")
		}
	} else {
		ok, err := lock.TryRLock()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("the cache is currently locked")
		}
	}

	return c, nil
}

func (c *LocalCache) ReadExistingCacheVersion() string {
	b, err := os.ReadFile(path.Join(c.dir, VersionFile))
	if entities.IsNotExist(err) {
		return "0.0.0"
	}
	return strings.TrimSpace(string(b))
}

func (c *LocalCache) CompatibleExistingCache() bool {
	return c.ReadExistingCacheVersion() == CacheVersion
}

func (c *LocalCache) Lock() error {
	ok, err := c.cacheLock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("could not lock the cache")
	}

	return nil
}

func (c *LocalCache) RLock() error {
	ok, err := c.cacheLock.TryRLock()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("could not rlock the cache")
	}
	return nil
}

func (c *LocalCache) Unlock() error {
	return c.cacheLock.Unlock()
}

func (c *LocalCache) Close() error {
	err := c.Unlock()
	if c.refStore != nil {
		err = errors.Join(err, c.refStore.Close())
	}

	return err
}

func (c *LocalCache) GetTempDir() string {
	return path.Join(c.dir, "tmp")
}

// OpenBlob implements entities.BlobStore
func (c *LocalCache) OpenBlob(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	return c.blobStore.OpenFile(ctx, key)
}

// Write implements entities.BlobStore
func (c *LocalCache) WriteBlob(ctx context.Context, key entities.Hash, reader io.Reader) error {
	return c.blobStore.Write(ctx, key, reader, nil)
}

func (c *LocalCache) HasBlob(ctx context.Context, key entities.Hash) (bool, error) {
	return c.blobStore.Has(key)
}

func (c *LocalCache) RemoveBlob(ctx context.Context, key entities.Hash) error {
	return c.blobStore.Remove(key)
}

// HasCASBlob checks if a CAS blob is already stored locally on disk under the blobs/
// directory (e.g. blobs/<sha256[:3]>/<sha256[3:]>) using its 64-character SHA-256 hex string.
// This fast disk probe avoids redundant network fetch roundtrips to RBE.
func (c *LocalCache) HasCASBlob(ctx context.Context, sha256Hex string) (bool, error) {
	return c.blobStore.HasHex(sha256Hex), nil
}

// WriteCASBlob streams and writes a CAS blob directly under the local cache's blobs/
// directory using its native SHA-256 hex string (e.g. blobs/<sha256[:3]>/<sha256[3:]>).
// Storing CAS blobs under this layout lets other consumers (e.g. FUSE passthrough)
// read them directly, eliminating the need for synthetic SHA-1 derivation,
// BadgerDB identity-store translation, or dual-key disk probing.
func (c *LocalCache) WriteCASBlob(ctx context.Context, sha256Hex string, reader io.Reader) error {
	return c.blobStore.WriteHex(ctx, sha256Hex, reader, nil)
}

func (c *LocalCache) OpenARI(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	return c.ariStore.OpenFile(ctx, key)
}

func (c *LocalCache) WriteARI(ctx context.Context, key entities.Hash, reader io.Reader) error {
	return c.ariStore.Write(ctx, key, reader, nil)
}

func (c *LocalCache) HasARI(ctx context.Context, key entities.Hash) (bool, error) {
	return c.ariStore.Has(key)
}

func (c *LocalCache) RemoveARI(ctx context.Context, key entities.Hash) error {
	return c.ariStore.Remove(key)
}

// OpenObjectFile implements entities.Object
func (c *LocalCache) OpenObjectFile(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	return c.objectStore.OpenFile(ctx, key)
}

// Get implements entities.ObjectStore
func (c *LocalCache) GetObject(ctx context.Context, key entities.Hash) ([]byte, error) {
	return c.objectStore.Get(ctx, key)
}

func (c *LocalCache) ObjectHeader(ctx context.Context, objectHash entities.Hash) (entities.ObjectHeader, error) {
	size, err := c.blobStore.Size(ctx, objectHash)
	if err == nil {
		return entities.ObjectHeader{
			Type:           entities.ObjectTypeBlob,
			Size:           size,
			CompressedSize: entities.BlobSizeAfterCompressed(size),
		}, nil
	}
	if !entities.IsNotExist(err) {
		return entities.ObjectHeader{}, err
	}
	size, err = c.ariStore.Size(ctx, objectHash)
	if err == nil {
		return entities.ObjectHeader{
			Type:           entities.ObjectTypeBlob,
			Size:           size,
			CompressedSize: entities.BlobSizeAfterCompressed(size),
		}, nil
	}
	if !entities.IsNotExist(err) {
		return entities.ObjectHeader{}, err
	}
	return c.objectStore.ObjectHeader(ctx, objectHash)
}

func (c *LocalCache) RemoveObject(ctx context.Context, key entities.Hash) error {
	return c.objectStore.Remove(key)
}

func (c *LocalCache) makeTreeMeta(ctx context.Context, content []byte) ([]byte, error) {
	if c.compressor != nil {
		var err error
		content, err = c.compressor.Decompress(content)
		if err != nil {
			return nil, err
		}
	}
	entries, err := entities.DecodeDirPlain(content)
	if err != nil {
		return nil, err
	}
	meta := make([]byte, 8*len(entries))
	for i, ent := range entries {
		size := int64(0)
		// there might be invalid git mode 100744, so we couldn't use IsFile() to check
		if ent.Mode != entities.FileModeDir && ent.Mode != entities.FileModeSubmodule {
			hdr, err := c.ObjectHeader(ctx, ent.Hash)
			if err != nil {
				return nil, err
			}
			size = hdr.Size
		}
		binary.LittleEndian.PutUint64(meta[i*8:], uint64(size))
	}
	return meta, nil
}

func (c *LocalCache) GetTree(ctx context.Context, key entities.Hash) ([]byte, entities.TreeMeta, error) {
	content, err := c.objectStore.Get(ctx, key)
	if err != nil {
		return nil, entities.TreeMeta{}, err
	}
	metaBuf, err := c.objectStore.GetMeta(ctx, key, TreeMetaTag)
	if err != nil {
		if !entities.IsNotExist(err) {
			return nil, entities.TreeMeta{}, entities.NotExistErrf("failed to get %s meta for %s", TreeMetaTag, key)
		}
		metaBuf, err = c.makeTreeMeta(ctx, content)
		if err != nil {
			return nil, entities.TreeMeta{}, err
		}
		if err := c.objectStore.PutMeta(ctx, key, TreeMetaTag, metaBuf); err != nil {
			return nil, entities.TreeMeta{}, err
		}
	}
	sizes := make([]int64, len(metaBuf)/8)
	for i := 0; i < len(metaBuf); i += 8 {
		sizes[i/8] = int64(binary.LittleEndian.Uint64(metaBuf[i : i+8]))
	}
	return content, entities.TreeMeta{Sizes: sizes}, nil
}

// Put implements entities.ObjectStore
func (c *LocalCache) PutObject(ctx context.Context, key entities.Hash, content []byte) error {
	return c.objectStore.Put(ctx, key, content)
}

func (c *LocalCache) PutTree(ctx context.Context, key entities.Hash, content []byte, meta entities.TreeMeta) error {

	metaBuf := make([]byte, 8*len(meta.Sizes))
	for i, s := range meta.Sizes {
		binary.LittleEndian.PutUint64(metaBuf[i*8:], uint64(s))
	}
	if err := c.objectStore.PutMeta(ctx, key, TreeMetaTag, metaBuf); err != nil {
		return err
	}

	return c.objectStore.Put(ctx, key, content)
}

func (c *LocalCache) MetaSize(ctx context.Context, treeHash entities.Hash) (int64, error) {
	return c.objectStore.MetaSize(ctx, treeHash, TreeMetaTag)
}

// Write implements entities.ObjectStore
func (c *LocalCache) WriteObject(ctx context.Context, key entities.Hash, reader io.Reader) error {
	return c.objectStore.Write(ctx, key, reader, nil)
}

func (c *LocalCache) MetaPath(ctx context.Context, key entities.Hash, meta string) string {
	return c.objectStore.PathMeta(key, meta)
}

func (c *LocalCache) HasObject(ctx context.Context, key entities.Hash) (bool, error) {
	return c.objectStore.Has(key)
}

// AddMapResult implements entities.MapStore
func (c *LocalCache) AddMapResult(ctx context.Context, from entities.Hash, to entities.Hash) error {
	return c.mapStore.AddMapResult(from, to)
}

// MapResults implements entities.MapStore
func (c *LocalCache) MapResults(ctx context.Context, id entities.Hash) ([]entities.Hash, error) {
	return c.mapStore.MapResults(id)
}

func (c *LocalCache) RefreshMapResults(ctx context.Context, id entities.Hash) error {
	return nil
}

func (c *LocalCache) RemoveMeta(ctx context.Context, key entities.Hash, meta string) error {
	return c.objectStore.RemoveMeta(key, meta)
}

// GetRefs implements entities.CASStore.
func (c *LocalCache) GetRefs(server string, project string, refs []string) ([]entities.Hash, error) {
	if c.refStore == nil {
		return nil, errors.New("no ref-store is set")
	}
	return c.refStore.Get(server, project, refs)
}

// PutRefs implements entities.CASStore.
func (c *LocalCache) PutRefs(server string, project string, refs []entities.RefHash) error {
	if c.refStore == nil {
		return errors.New("no ref-store is set")
	}
	return c.refStore.Put(server, project, refs)
}

// SearchRefs implements entities.CASStore.
func (c *LocalCache) SearchRefs(server string, project string, refPrefix string) ([]entities.ProjectRefHash, error) {
	if c.refStore == nil {
		return nil, errors.New("no ref-store is set")
	}
	return c.refStore.Search(server, project, refPrefix)
}

func (c *LocalCache) Stat(ctx context.Context, hash entities.Hash) (entities.StatInfo, error) {
	var err error
	var stat entities.StatInfo
	hdr, err := c.ObjectHeader(context.Background(), hash)
	if err != nil {
		return stat, err
	}

	switch hdr.Type {
	case entities.ObjectTypeBlob:
		if stat, err = c.blobStore.Stat(ctx, hash); err == nil {
			stat.GitType = hdr.Type
			return stat, err
		}
		if stat, err = c.ariStore.Stat(ctx, hash); err == nil {
			stat.GitType = hdr.Type
			return stat, err
		}
	default:
		if stat, err = c.objectStore.Stat(ctx, hash); err == nil {
			stat.GitType = hdr.Type
			return stat, err
		}
	}

	return stat, err
}
