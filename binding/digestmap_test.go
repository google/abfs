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
	"testing"

	"github.com/google/abfs/shared/entities"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDigestMapRequestCoding(t *testing.T) {
	require := require.New(t)

	digestMapRequest := &DigestMapRequest{
		Version: CurrentDigestMapRequestVersion,
		Tree:    entities.EmptyDirHash,
	}

	err := digestMapRequest.Encode()
	require.NoError(err)

	digestMapRequest2 := &DigestMapRequest{}
	err = digestMapRequest2.Decode(digestMapRequest.Hash, digestMapRequest.Encoded)
	require.NoError(err)
	assert.Equal(t, digestMapRequest, digestMapRequest2)
}

func TestDigestMapCoding(t *testing.T) {
	require := require.New(t)

	digestMap := &DigestMap{
		Version: CurrentDigestMapVersion,
		Digests: []DigestEntry{
			{Hash: entities.EmptyFileHash, SHA256: entities.ZeroSHA256},
		},
		DigestMaps: []Hash{entities.EmptyFileHash},
	}

	err := digestMap.Encode()
	require.NoError(err)

	digestMap2 := &DigestMap{}
	err = digestMap2.Decode(digestMap.Hash, digestMap.Encoded)
	require.NoError(err)
	assert.Equal(t, digestMap, digestMap2)
}

func TestDigestMapLinks(t *testing.T) {
	require := require.New(t)

	req := &DigestMapRequest{
		Tree: entities.EmptyFileHash,
	}
	require.ElementsMatch([]Hash{entities.EmptyFileHash}, req.Links())

	h1 := entities.NewHash("0123456789012345678901234567890123456789")
	h2 := entities.NewHash("abcdefabcdefabcdefabcdefabcdefabcdefabcd")
	dm := &DigestMap{
		Digests: []DigestEntry{
			{Hash: h1, SHA256: entities.ZeroSHA256},
		},
		DigestMaps: []Hash{h2},
	}
	require.ElementsMatch([]Hash{h2, h1}, dm.Links())
}

type fakeDigestStore struct {
	entities.CASStore
	objects map[entities.Hash][]byte
	maps    map[entities.Hash][]entities.Hash
}

func newFakeDigestStore() *fakeDigestStore {
	return &fakeDigestStore{
		objects: make(map[entities.Hash][]byte),
		maps:    make(map[entities.Hash][]entities.Hash),
	}
}

func (f *fakeDigestStore) GetObject(ctx context.Context, hash entities.Hash) ([]byte, error) {
	if b, ok := f.objects[hash]; ok {
		return b, nil
	}
	return nil, entities.NotExistErrf("object %s not found", hash)
}

func (f *fakeDigestStore) MapResults(ctx context.Context, hash entities.Hash) ([]entities.Hash, error) {
	if res, ok := f.maps[hash]; ok {
		return res, nil
	}
	return nil, nil
}

func (f *fakeDigestStore) RefreshMapResults(ctx context.Context, hash entities.Hash) error {
	return nil
}

func TestGetDigestMap(t *testing.T) {
	ctx := context.Background()
	store := newFakeDigestStore()

	tree := entities.NewHash("1111111111111111111111111111111111111111")
	blob1 := entities.NewHash("2222222222222222222222222222222222222222")
	sha1 := entities.NewSHA256("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	// Missing tree returns not exist
	dm, err := GetDigestMap(ctx, tree, store)
	require.Error(t, err)
	require.True(t, entities.IsNotExist(err))
	require.Nil(t, dm)

	// Populate store with DigestMap
	req := &DigestMapRequest{
		Version: CurrentDigestMapRequestVersion,
		Tree:    tree,
	}
	require.NoError(t, req.Encode())

	targetDM := &DigestMap{
		Version: CurrentDigestMapVersion,
		Digests: []DigestEntry{
			{Hash: blob1, SHA256: sha1},
		},
	}
	require.NoError(t, targetDM.Encode())

	store.maps[req.Hash] = []entities.Hash{targetDM.Hash}
	store.objects[targetDM.Hash] = targetDM.Encoded

	loadedDM, err := GetDigestMap(ctx, tree, store)
	require.NoError(t, err)
	require.NotNil(t, loadedDM)
	require.Equal(t, targetDM.Hash, loadedDM.Hash)
	require.Len(t, loadedDM.Digests, 1)
	require.Equal(t, blob1, loadedDM.Digests[0].Hash)
	require.Equal(t, sha1, loadedDM.Digests[0].SHA256)
}
