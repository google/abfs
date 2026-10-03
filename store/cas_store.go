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

package store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cas/oms/git"
	"github.com/google/abfs/cas/hash/hashers/sha1"

	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/store/localcache"
)

var ErrNoProvider = errors.New("no remote providers are enabled")

type CASStore struct {
	local     *localcache.LocalCache
	localOnly bool
	provider  *Provider
	cas       cas.Store
}

var _ entities.CASStore = (*CASStore)(nil)

type CASStoreOpts struct {
	CacheDir     string
	LocalOnly    bool
	FetchTimeout time.Duration
	FsyncObjects bool
}

func newGitCASStore(root string, casProvider cas.Provider) (cas.Store, error) {
	// Create the CAS store for file system
	hasher := sha1.NewHasher()
	local := git.NewCASStore(root, "abfs", hasher, false)

	// We are borrowing the casProvider here so don't want to open that as
	// this is already done, instead open the local store only
	if err := local.Open(); err != nil {
		return nil, err
	}
	var store cas.Store
	if casProvider != nil {
		store = cas.NewRemote(local, casProvider)
	} else {
		store = local
	}
	return store, nil

}

// Alternative factory function to avoid callsite thrash.
func NewCASStoreWithOpts(opts CASStoreOpts) (*CASStore, error) {
	c := new(CASStore)

	lcOpts := []localcache.CacheOpt{localcache.PlainEmptyObjectsOpt(), localcache.FsyncOpt(opts.FsyncObjects)}
	c.provider = NewProvider(opts.CacheDir, opts.FetchTimeout, opts.LocalOnly)
	if err := c.provider.Start(); err != nil {
		return nil, err
	}

	if !opts.LocalOnly {
		lcOpts = append(lcOpts, localcache.NotifyPutBlobOpt(func(ctx context.Context, key entities.Hash) error {
			return c.provider.NewBlob(ctx, key)
		}))

		lcOpts = append(lcOpts, localcache.NotifyReadOpt(func(ctx context.Context, key entities.Hash) error {
			return c.provider.SendNotifyUse(key)
		}))
	}
	local, err := localcache.NewCache(opts.CacheDir, lcOpts...)
	if err != nil {
		return nil, err
	}
	c.local = local
	c.localOnly = opts.LocalOnly
	var provider cas.Provider = nil
	if !opts.LocalOnly {
		provider = c.provider.casProvider
	}
	c.cas, err = newGitCASStore(opts.CacheDir, provider)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func NewCASStore(cacheDir string, localOnly bool, fetchTimeout time.Duration) (*CASStore, error) {
	return NewCASStoreWithOpts(CASStoreOpts{
		CacheDir:     cacheDir,
		LocalOnly:    localOnly,
		FetchTimeout: fetchTimeout,
		FsyncObjects: true,
	})
}

func (c *CASStore) CAS() cas.Store {
	return c.cas
}
func (c *CASStore) Close() error {
	errs := make([]error, 0)
	errs = append(errs, c.local.Close())
	if c.cas != nil {
		errs = append(errs, c.cas.Close())
		// cas.Close() -- closes the provider so don't repeat
	} else if c.provider != nil {
		errs = append(errs, c.provider.Close())
	}
	return errors.Join(errs...)
}

func (c *CASStore) Local() *localcache.LocalCache {
	return c.local
}

func (c *CASStore) Provider() *Provider {
	return c.provider
}

func (c *CASStore) CachemanClient() *client.Client {
	return c.provider.client
}

// OpenBlob implements entities.CASStore
func (c *CASStore) OpenBlob(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	r, err := c.local.OpenBlob(ctx, key)
	if !entities.IsNotExist(err) || c.localOnly {
		return r, err
	}
	if err := c.provider.FetchBlob(ctx, key); err != nil {
		return nil, err
	}
	return c.local.OpenBlob(ctx, key)
}

// Write implements entities.CASStore
func (c *CASStore) WriteBlob(ctx context.Context, key entities.Hash, size int64, reader io.Reader) error {
	err := c.local.WriteBlob(ctx, key, reader)
	if err != nil || c.localOnly {
		return err
	}
	return c.provider.NewBlob(ctx, key)
}

// GetTree implements entities.CASStore.
func (c *CASStore) GetTree(ctx context.Context, key entities.Hash) ([]byte, entities.TreeMeta, error) {
	buf, meta, err := c.local.GetTree(ctx, key)
	if !entities.IsNotExist(err) || c.localOnly {
		return buf, meta, err
	}
	if err := c.provider.FetchTree(ctx, key); err != nil {
		return nil, entities.TreeMeta{}, err
	}
	return c.local.GetTree(ctx, key)
}

// PutTree implements entities.CASStore.
func (c *CASStore) PutTree(ctx context.Context, key entities.Hash, content []byte, meta entities.TreeMeta) error {
	err := c.local.PutTree(ctx, key, content, meta)
	if err != nil || c.localOnly {
		return nil
	}
	return c.provider.NewTree(ctx, key)
}

// OpenObjectFile implements entities.CASStore
func (c *CASStore) OpenObjectFile(ctx context.Context, key entities.Hash) (io.ReadCloser, error) {
	r, err := c.local.OpenObjectFile(ctx, key)
	if !entities.IsNotExist(err) || c.localOnly {
		return r, err
	}
	if err := c.provider.FetchObject(ctx, key); err != nil {
		return nil, err
	}
	return c.local.OpenObjectFile(ctx, key)
}

// Get implements entities.CASStore
func (c *CASStore) GetObject(ctx context.Context, key entities.Hash) ([]byte, error) {
	buf, err := c.local.GetObject(ctx, key)
	if !entities.IsNotExist(err) || c.localOnly {
		return buf, err
	}
	if err := c.provider.FetchObject(ctx, key); err != nil {
		return nil, err
	}
	return c.local.GetObject(ctx, key)
}

// Put implements entities.CASStore
func (c *CASStore) PutObject(ctx context.Context, key entities.Hash, content []byte) error {
	err := c.local.PutObject(ctx, key, content)
	if err != nil || c.localOnly {
		return err
	}
	return c.provider.NewObject(ctx, key)
}

// Write implements entities.CASStore
func (c *CASStore) WriteObject(ctx context.Context, key entities.Hash, reader io.Reader) error {
	err := c.local.WriteObject(ctx, key, reader)
	if err != nil || c.localOnly {
		return err
	}
	return c.provider.NewObject(ctx, key)
}

// ObjectHeader implements entities.CASStore
func (c *CASStore) ObjectHeader(ctx context.Context, key entities.Hash) (entities.ObjectHeader, error) {
	hdr, err := c.local.ObjectHeader(ctx, key)
	if !entities.IsNotExist(err) || c.localOnly {
		return hdr, err
	}
	// first try to get info from the cacheman
	if stat, err := c.Stat(ctx, key); err == nil {
		if stat.GitType == entities.ObjectTypeBlob {
			var ret entities.ObjectHeader
			ret.Type = entities.ObjectTypeBlob
			ret.Size = stat.Size
			ret.CompressedSize = entities.BlobSizeAfterCompressed(stat.Size)
			return ret, err
		} else {
			// prefetch mechanism only works for blob, the stat call will still
			// fetch object to local, so we should be able to get it directly.
			if hdr, err := c.local.ObjectHeader(ctx, key); err == nil {
				return hdr, err
			} else if !entities.IsNotExist(err) {
				return hdr, err
			}
		}
	}

	// we still fetch the object to local no matter why above statStore didn't
	// work
	if err := c.provider.FetchObject(ctx, key); err != nil {
		return hdr, err
	}
	return c.local.ObjectHeader(ctx, key)
}

// Stat implements entities.CASStore
func (c *CASStore) Stat(ctx context.Context, key entities.Hash) (entities.StatInfo, error) {
	info, err := c.local.Stat(ctx, key)
	if !entities.IsNotExist(err) || c.localOnly {
		return info, err
	}

	return c.provider.FetchStat(ctx, key)
}

// KeepAlive implements entities.CASStore
func (c *CASStore) KeepAlive(ctx context.Context, key entities.Hash) error {
	return c.provider.KeepAlive(ctx, key)
}

// AddMapResult implements entities.CASStore
func (c *CASStore) AddMapResult(ctx context.Context, from, to entities.Hash) error {
	return c.provider.AddMapResult(ctx, from, to)
}

// MapResults implements entities.CASStore
func (c *CASStore) MapResults(ctx context.Context, id entities.Hash) ([]entities.Hash, error) {
	return c.provider.MapResults(ctx, id)
}

// RefreshMapResults implements entities.CASStore
func (c *CASStore) RefreshMapResults(ctx context.Context, id entities.Hash) error {
	if c.localOnly {
		return nil
	}
	return c.provider.FetchMapResults(ctx, id)
}

// GetRefs implements entities.CASStore.
func (c *CASStore) GetRefs(server string, project string, refs []string) ([]entities.Hash, error) {
	if c.localOnly {
		return nil, errors.New("cannot get refs in localOnly mode")
	}
	return c.provider.GetRefs(server, project, refs)
}

// PutRefs implements entities.CASStore.
func (c *CASStore) PutRefs(server string, project string, refs []entities.RefHash) error {
	if c.localOnly {
		return errors.New("cannot put refs in localOnly mode")
	}
	return c.provider.PutRefs(server, project, refs, false)
}

// SearchRefs implements entities.CASStore.
func (c *CASStore) SearchRefs(server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	if c.localOnly {
		return nil, errors.New("cannot search refs in localOnly mode")
	}

	return c.provider.SearchRefs(server, project, refPrefix, limit)
}

// IsObjectRemote implements entities.CASStore
func (c *CASStore) IsObjectRemote(ctx context.Context, key entities.Hash) (bool, error) {
	return c.provider.IsObjectRemote(ctx, key)
}

// StartGC implements entities.CASStore
func (c *CASStore) StartGC(ctx context.Context) error {
	return c.provider.StartGC(ctx)
}

func (c *CASStore) HealthCheck() error {
	var errs []error
	if err := c.provider.Ping(); err != nil {
		errs = append(errs, err)
	}
	if err := c.provider.PingBinary(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// StoreBlob writes a blob to the store and returns its hash.
func StoreBlob(ctx context.Context, store entities.BlobStore, content []byte) (entities.Hash, int64, error) {
	hash := objects.HashBlobBytes(content)
	size := int64(len(content))
	err := store.WriteBlob(ctx, hash, size, bytes.NewBuffer(content))
	return hash, size, err
}

func ReadAllBlob(ctx context.Context, store entities.BlobStore, hash entities.Hash) (content []byte, retErr error) {
	f, err := store.OpenBlob(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	return io.ReadAll(f)
}
