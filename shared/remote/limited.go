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
	"errors"
	"sync"
	"sync/atomic"

	"github.com/google/abfs/shared/entities"
)

type token struct{}
type LimitedRemoteProvider struct {
	mu         sync.Mutex
	c          RemoteCloser
	sem        chan token
	blockedCnt atomic.Int32
}

var _ RemoteCloser = (*LimitedRemoteProvider)(nil)

func NewLimitedRemote(c RemoteCloser, concurrencyLimit int) *LimitedRemoteProvider {
	return &LimitedRemoteProvider{
		c:   c,
		sem: make(chan token, concurrencyLimit),
	}
}

type concurrencyLimitedFunc func(ctx context.Context) error
type concurrencyLimitedFuncWithReturn[T any] func(ctx context.Context) (T, error)

func withConcurrencyLimit(ctx context.Context, sem chan token, fn concurrencyLimitedFunc) error {
	select {
	case sem <- token{}:
		defer func() { <-sem }()
		return fn(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func withConcurrencyLimitWithReturn[T any](ctx context.Context, sem chan token, fn concurrencyLimitedFuncWithReturn[T]) (T, error) {
	var zero T // zero value of type T
	select {
	case sem <- token{}:
		defer func() { <-sem }()
		return fn(ctx)
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// SetLimit changes the concurrency limit. Operations already in progress or
// waiting to acquire a token on the old semaphore will continue to use the old
// limit. New operations will use the new limit. This means it is possible to
// temporarily exceed the limit.
func (r *LimitedRemoteProvider) SetLimit(limit int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if limit == cap(r.sem) {
		return
	}
	r.sem = make(chan token, limit)
}

func (r *LimitedRemoteProvider) Limit() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cap(r.sem)
}

func (r *LimitedRemoteProvider) getSem() chan token {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sem
}

// Headers implements RemoteProvider.
func (r *LimitedRemoteProvider) Headers(ctx context.Context, keys []Hash) ([]entities.ObjectHeader, error) {
	r.blockedCnt.Add(1)

	return withConcurrencyLimitWithReturn(ctx, r.getSem(), func(ctx context.Context) ([]entities.ObjectHeader, error) {
		r.blockedCnt.Add(-1)

		return r.c.Headers(ctx, keys)
	})
}

// SHA256 implements RemoteProvider.
func (r *LimitedRemoteProvider) SHA256(ctx context.Context, keys []Hash) ([]entities.SHA256, error) {
	r.blockedCnt.Add(1)

	return withConcurrencyLimitWithReturn(ctx, r.getSem(), func(ctx context.Context) ([]entities.SHA256, error) {
		r.blockedCnt.Add(-1)

		return r.c.SHA256(ctx, keys)
	})
}

// GetObject implements RemoteProvider.
func (r *LimitedRemoteProvider) GetObject(ctx context.Context, key Hash) (ContentReadCloser, error) {
	r.blockedCnt.Add(1)

	return withConcurrencyLimitWithReturn(ctx, r.getSem(), func(ctx context.Context) (ContentReadCloser, error) {
		r.blockedCnt.Add(-1)

		return r.c.GetObject(ctx, key)
	})
}

// PutObject implements RemoteProvider.
func (r *LimitedRemoteProvider) PutObject(ctx context.Context, key Hash, reader ContentReadCloser) error {
	r.blockedCnt.Add(1)

	return withConcurrencyLimit(ctx, r.getSem(), func(ctx context.Context) error {
		r.blockedCnt.Add(-1)

		return r.c.PutObject(ctx, key, reader)
	})
}

// AddMapResult implements RemoteProvider.
func (r *LimitedRemoteProvider) AddMapResult(ctx context.Context, from entities.Hash, to entities.Hash) error {
	r.blockedCnt.Add(1)

	return withConcurrencyLimit(ctx, r.getSem(), func(ctx context.Context) error {
		r.blockedCnt.Add(-1)

		return r.c.AddMapResult(ctx, from, to)
	})
}

// MapResults implements RemoteProvider.
func (r *LimitedRemoteProvider) MapResults(ctx context.Context, hash entities.Hash) ([]entities.Hash, error) {
	r.blockedCnt.Add(1)

	return withConcurrencyLimitWithReturn(ctx, r.getSem(), func(ctx context.Context) ([]entities.Hash, error) {
		r.blockedCnt.Add(-1)

		return r.c.MapResults(ctx, hash)
	})
}

// Mark implements RemoteProvider.
func (r *LimitedRemoteProvider) Mark(ctx context.Context, hash entities.Hash, key string, value string) (int64, error) {
	r.blockedCnt.Add(1)

	return withConcurrencyLimitWithReturn(ctx, r.getSem(), func(ctx context.Context) (int64, error) {
		r.blockedCnt.Add(-1)

		return r.c.Mark(ctx, hash, key, value)
	})
}

// GetRefs implements RemoteProvider.
func (r *LimitedRemoteProvider) GetRefs(ctx context.Context, server string, project string, refs []string) ([]Hash, error) {
	r.blockedCnt.Add(1)

	return withConcurrencyLimitWithReturn(ctx, r.getSem(), func(ctx context.Context) ([]entities.Hash, error) {
		r.blockedCnt.Add(-1)

		return r.c.GetRefs(ctx, server, project, refs)
	})
}

// PutRefs implements RemoteProvider.
func (r *LimitedRemoteProvider) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	r.blockedCnt.Add(1)

	return withConcurrencyLimit(ctx, r.getSem(), func(ctx context.Context) error {
		r.blockedCnt.Add(-1)

		return r.c.PutRefs(ctx, server, project, refs)
	})
}

// SearchRefs implements RemoteProvider.
func (r *LimitedRemoteProvider) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	r.blockedCnt.Add(1)

	return withConcurrencyLimitWithReturn(ctx, r.getSem(), func(ctx context.Context) ([]entities.ProjectRefHash, error) {
		r.blockedCnt.Add(-1)

		return r.c.SearchRefs(ctx, server, project, refPrefix, limit)
	})
}

// Version implements RemoteProvider.
func (r *LimitedRemoteProvider) Version(ctx context.Context) (string, error) {
	r.blockedCnt.Add(1)

	return withConcurrencyLimitWithReturn(ctx, r.getSem(), func(ctx context.Context) (string, error) {
		r.blockedCnt.Add(-1)

		return r.c.Version(ctx)
	})
}

// WhoAmI implements RemoteProvider.
func (r *LimitedRemoteProvider) WhoAmI(ctx context.Context) (string, error) {
	return "", errors.New("unimplemented")
}

// Close implements RemoteCloser.
func (r *LimitedRemoteProvider) Close() error {
	return r.c.Close()
}

func (r *LimitedRemoteProvider) MaxConcurrent() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cap(r.sem)
}

func (r *LimitedRemoteProvider) ActiveRequests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sem)
}

func (r *LimitedRemoteProvider) BlockedRequests() int {
	return int(r.blockedCnt.Load())
}
