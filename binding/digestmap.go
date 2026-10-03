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

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const (
	CurrentDigestMapRequestVersion                     = "0.1"
	ObjectTypeDigestMapRequest     entities.ObjectType = "digestmap-request"
)

type SHA256 = entities.SHA256

type DigestMapRequest struct {
	JSONBaseObject
	Version string
	Tree    Hash
}

func init() {
	objects.Factory.Register(ObjectTypeDigestMapRequest, func() Object {
		return new(DigestMapRequest)
	})
}

func (d *DigestMapRequest) Encode() error {
	return ObjectJSONEncode(d, ObjectTypeDigestMapRequest)
}

func (d *DigestMapRequest) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(d, ObjectTypeDigestMapRequest, hash, content)
}

func (d *DigestMapRequest) Links() []Hash {
	return []Hash{d.Tree}
}

const (
	CurrentDigestMapVersion                     = "0.1"
	ObjectTypeDigestMap     entities.ObjectType = "digestmap"

	DigestMapThreshold = 1000
)

type DigestMap struct {
	JSONBaseObject
	Version string
	// Digests' order is arbitrary and can't be relied on.
	Digests    []DigestEntry
	DigestMaps []Hash
}

type DigestEntry struct {
	Hash   Hash
	SHA256 entities.SHA256
}

func init() {
	objects.Factory.Register(ObjectTypeDigestMap, func() Object {
		return new(DigestMap)
	})
}

func (d *DigestMap) Encode() error {
	return ObjectJSONEncode(d, ObjectTypeDigestMap)
}

func (d *DigestMap) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(d, ObjectTypeDigestMap, hash, content)
}

func (d *DigestMap) Links() []Hash {
	hashes := make([]Hash, 0, len(d.DigestMaps)+len(d.Digests))
	hashes = append(hashes, d.DigestMaps...)
	for _, de := range d.Digests {
		hashes = append(hashes, de.Hash)
	}
	return hashes
}

// GetDigestMap fetches and decodes the DigestMap associated with the tree hash from the store.
func GetDigestMap(ctx context.Context, tree entities.Hash, store entities.CASStore) (*DigestMap, error) {
	mapResult, err := GetDigestMapResult(ctx, store, tree)
	if err != nil {
		return nil, fmt.Errorf("failed to get DigestMap result for tree %s: %w", tree, err)
	}
	outBytes, err := store.GetObject(ctx, mapResult)
	if err != nil {
		return nil, fmt.Errorf("failed to get DigestMap object %s: %w", mapResult, err)
	}
	dm := new(DigestMap)
	if err := dm.Decode(mapResult, outBytes); err != nil {
		return nil, fmt.Errorf("failed to decode DigestMap %s: %w", mapResult, err)
	}
	return dm, nil
}

func GetDigestMapResult(ctx context.Context, store entities.CASStore, tree entities.Hash) (entities.Hash, error) {
	req := &DigestMapRequest{
		Version: CurrentDigestMapRequestVersion,
		Tree:    tree,
	}
	if err := req.Encode(); err != nil {
		return entities.ZeroHash, fmt.Errorf("failed to encode DigestMapRequest: %w", err)
	}
	for i := 0; i < 2; i++ {
		mapResults, err := store.MapResults(ctx, req.Hash)
		if err != nil {
			return entities.ZeroHash, fmt.Errorf("failed to get map results for DigestMapRequest %s: %w", req.Hash, err)
		}
		if len(mapResults) > 0 {
			return mapResults[0], nil
		}
		if i == 0 {
			if err := store.RefreshMapResults(ctx, req.Hash); err != nil {
				return entities.ZeroHash, fmt.Errorf("failed to refresh map results for DigestMapRequest %s: %w", req.Hash, err)
			}
		}
	}
	return entities.ZeroHash, entities.NotExistErrf("digest-maps for req=%s not found", req.Hash)
}
