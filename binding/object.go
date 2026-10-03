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

package binding

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
)

type ObjectType = entities.ObjectType

func FetchObject[T any](ctx context.Context, store entities.CASStore, hash Hash) (*T, error) {
	content, err := store.GetObject(ctx, hash)
	if err != nil {
		return nil, err
	}

	var r T
	err = any(&r).(Object).Decode(hash, content)
	return &r, err
}

func FetchObjectWithRetry[T any](ctx context.Context, store entities.CASStore, h entities.Hash, timeout time.Duration) (obj *T, err error) {
	const maxRetries = 20
	start := time.Now()
	for i := maxRetries; i > 0; i-- {
		resp, err := FetchObject[T](ctx, store, h)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if i == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(timeout >> i):
		}
	}
	return nil, fmt.Errorf("failed to fetch %T with hash %s after %s: %w", obj, h, time.Since(start), err)
}

func FetchObjectWarnOnRetry[T any](ctx context.Context, store entities.CASStore, h entities.Hash, timeout time.Duration) (*T, error) {
	obj, err := FetchObject[T](ctx, store, h)
	if err == nil { // On NO errors
		return obj, nil
	}

	if !entities.IsNotExist(err) {
		return nil, err
	}
	log.Printf("ERROR: failed to fetch %s on the first try, going to retry...", h)
	return FetchObjectWithRetry[T](ctx, store, h, timeout)
}

func FetchMultiObject[T any](ctx context.Context, store entities.CASStore, hashes []entities.Hash) ([]*T, error) {
	result := make([]*T, len(hashes))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for i, h := range hashes {
		if h == entities.ZeroHash {
			continue
		}
		g.Go(func() error {
			obj, err := FetchObject[T](ctx, store, h)
			if err != nil {
				return err
			}
			result[i] = obj
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return result, nil
}
