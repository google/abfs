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

package maps

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/google/abfs/shared/entities"
)

type Hash = entities.Hash

type MapObject interface {
	GetHash() Hash
	GetEncoded() []byte
	Encode() error
	Decode(Hash, []byte) error
}

type MapRequest struct {
	Input        MapObject
	storeInput   bool
	MapFunc      func(MapObject) (MapObject, error)
	Validator    func(MapObject) (bool, error)
	OutputDecode func(hash Hash, content []byte) (MapObject, error)
	Store        entities.CASStore
	InvalidCount int
	CacheHit     bool
	StartTime    time.Time
	Duration     time.Duration
}

// MapInput() encode the input, if 'storeInput' is specified it stores the input
// object to the object-store as well.
func MapInput(ctx context.Context, req *MapRequest) (Hash, error) {
	var inHash Hash
	req.StartTime = time.Now()
	defer func() {
		req.Duration += time.Since(req.StartTime)
	}()
	if err := req.Input.Encode(); err != nil {
		return inHash, fmt.Errorf("cannot encode input: %w", err)
	}
	inHash = req.Input.GetHash()

	return inHash, nil
}

// MapExisting() find the right outputs for a given input. Checks the cache to see if it finds any
// previously stored results.
func MapExisting(ctx context.Context, inHash Hash, req *MapRequest) (MapObject, error) {
	startTime := time.Now()
	defer func() {
		req.Duration += time.Since(startTime)
	}()
	tried := make(map[entities.Hash]bool)
	hasMapEntry := false
	for try := 0; try < 2; try++ {
		hashes, err := req.Store.MapResults(ctx, inHash)
		if err != nil {
			return nil, fmt.Errorf("failed to find the map result for %s: %w", inHash, err)
		}

		for _, outHash := range hashes {
			hasMapEntry = true
			if tried[outHash] {
				continue
			}
			tried[outHash] = true

			outBytes, err := req.Store.GetObject(ctx, outHash)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch object %s which is map result of %s: %w", outHash, inHash, err)
			}
			out, err := req.OutputDecode(outHash, outBytes)
			if err != nil {
				return nil, fmt.Errorf("failed to decode object %s which is map result of %s: %w", outHash, inHash, err)
			}
			match := true
			if req.Validator != nil {
				match, err = req.Validator(out)
				if err != nil {
					return nil, fmt.Errorf("failed to validate response %s which is map result of %s: %w", outHash, inHash, err)
				}
			}
			if match {
				req.CacheHit = true
				return out, nil
			}
			req.InvalidCount++
		}
		if try == 0 {
			if err := req.Store.RefreshMapResults(ctx, inHash); err != nil {
				return nil, fmt.Errorf("failed to refresh map result for %s: %w", inHash, err)
			}
		}
	}
	if !hasMapEntry {
		req.storeInput = true
	}
	return nil, nil
}

// MapExecute() run the mapFunc() to find a new result and stores it in the map and object-store.
func MapExecute(ctx context.Context, inHash Hash, req *MapRequest) (MapObject, error) {
	startTime := time.Now()
	defer func() {
		req.Duration += time.Since(startTime)
	}()
	var outHash entities.Hash
	out, mapErr := req.MapFunc(req.Input)
	// MapFunc can return a MapObject even when error happens. We try to write the out object to
	// store, so that user can know what has gone wrong even on error.
	// Comparing to nil does not work, because MapFuncs may return a typed nil.
	// For interfaces, (type, value) must both be nil to correctly compare against nil.
	if out != nil && !reflect.ValueOf(out).IsNil() {
		if req.storeInput {
			if err := req.Store.PutObject(ctx, inHash, req.Input.GetEncoded()); err != nil {
				return out, fmt.Errorf("cannot store input object with hash %s: %w", inHash, err)
			}
		}

		if err := out.Encode(); err != nil {
			return out, fmt.Errorf("cannot encode object response to %s: %w", inHash, err)
		}
		outHash = out.GetHash()
		// TODO Give callers the option of storing the output in the background
		if err := req.Store.PutObject(ctx, outHash, out.GetEncoded()); err != nil {
			return out, fmt.Errorf("cannot store object with hash %s response to %s: %w", outHash, inHash, err)
		}
	}
	if mapErr != nil {
		// we want to print out the error from the command first to match ninja as closely as possible
		return out, fmt.Errorf("%w\nabfs-maps: above error from running map func for request=%s result=%s", mapErr, inHash, outHash)
	}

	if err := req.Store.AddMapResult(ctx, inHash, outHash); err != nil {
		return out, fmt.Errorf("cannot add a map from %s to %s: %w", inHash, outHash, err)
	}

	return out, nil
}

func Map(ctx context.Context, req *MapRequest) (o MapObject, err error) {
	inHash, err := MapInput(ctx, req)
	if err != nil {
		return nil, err
	}
	out, err := MapExisting(ctx, inHash, req)
	if err != nil {
		return nil, err
	}
	if out != nil {
		return out, nil
	}
	return MapExecute(ctx, inHash, req)
}
