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

package fetch

// import (
// 	"context"
// 	"sort"
// 	"testing"

// 	"github.com/google/abfs/binding"
// 	"github.com/google/abfs/shared/entities"
// 	"github.com/google/abfs/shared/remote"

// 	"github.com/stretchr/testify/require"
// )

// /*
// Testdata description.
// All hashes are a single hex digit followed by zeros, and will be referenced
// by their single non-zero digit.
//              1(commit)
//             /          \
//         2(commit)     3(tree)
//         /              /      \
//    4(tree)        5(blob)  6(tree)
//      /                          \
//   7(blob)                     8(blob)
// */

// var (
// 	hash1 = entities.NewHash("1000000000000000000000000000000000000000")
// 	hash2 = entities.NewHash("2000000000000000000000000000000000000000")
// 	hash3 = entities.NewHash("3000000000000000000000000000000000000000")
// 	hash4 = entities.NewHash("4000000000000000000000000000000000000000")
// 	hash5 = entities.NewHash("5000000000000000000000000000000000000000")
// 	hash6 = entities.NewHash("6000000000000000000000000000000000000000")
// 	hash7 = entities.NewHash("7000000000000000000000000000000000000000")
// 	hash8 = entities.NewHash("8000000000000000000000000000000000000000")
// )

// var (
// 	hash1Links = []binding.Link{
// 		{
// 			ObjectType: gitobjects.ObjectTypeCommit,
// 			Hash:       hash2,
// 		},
// 		{
// 			ObjectType: gitobjects.ObjectTypeDir,
// 			Hash:       hash3,
// 		},
// 	}
// 	hash2Links = []binding.Link{
// 		{
// 			ObjectType: gitobjects.ObjectTypeDir,
// 			Hash:       hash4,
// 		},
// 	}
// 	hash3Links = []binding.Link{
// 		{
// 			ObjectType: binding.ObjectTypeBlob,
// 			Hash:       hash5,
// 		},
// 		{
// 			ObjectType: gitobjects.ObjectTypeDir,
// 			Hash:       hash6,
// 		},
// 	}
// 	hash4Links = []binding.Link{
// 		{
// 			ObjectType: binding.ObjectTypeBlob,
// 			Hash:       hash7,
// 		},
// 	}
// 	hash5Links = []binding.Link{}
// 	hash6Links = []binding.Link{
// 		{
// 			ObjectType: binding.ObjectTypeBlob,
// 			Hash:       hash8,
// 		},
// 	}
// 	hash7Links = []binding.Link{}
// 	hash8Links = []binding.Link{}
// )

// type fakeObject struct {
// 	hash Hash
// }

// func (f *fakeObject) GetHash() Hash {
// 	return f.hash
// }

// func (f *fakeObject) GetEncoded() []byte {
// 	panic("not implemented")
// }

// func (f *fakeObject) Encode() error {
// 	panic("not implemented")
// }

// func (f *fakeObject) Decode(Hash, []byte) error {
// 	panic("not implemented")
// }

// func (f *fakeObject) Links() []binding.Link {
// 	switch f.hash {
// 	case hash1:
// 		return hash1Links
// 	case hash2:
// 		return hash2Links
// 	case hash3:
// 		return hash3Links
// 	case hash4:
// 		return hash4Links
// 	case hash5:
// 		return hash5Links
// 	case hash6:
// 		return hash6Links
// 	case hash7:
// 		return hash7Links
// 	case hash8:
// 		return hash8Links
// 	default:
// 		panic("bad hash")
// 	}
// }

// func decoder(ctx context.Context, hash Hash) (binding.ObjectType, binding.Object, error) {
// 	switch hash {
// 	case hash1:
// 		return gitobjects.ObjectTypeCommit, &fakeObject{hash: hash1}, nil
// 	case hash2:
// 		return gitobjects.ObjectTypeCommit, &fakeObject{hash: hash2}, nil
// 	case hash3:
// 		return gitobjects.ObjectTypeCommit, &fakeObject{hash: hash3}, nil
// 	case hash4:
// 		return gitobjects.ObjectTypeCommit, &fakeObject{hash: hash4}, nil
// 	case hash5:
// 		return gitobjects.ObjectTypeCommit, &fakeObject{hash: hash5}, nil
// 	case hash6:
// 		return gitobjects.ObjectTypeCommit, &fakeObject{hash: hash6}, nil
// 	case hash7:
// 		return gitobjects.ObjectTypeCommit, &fakeObject{hash: hash7}, nil
// 	case hash8:
// 		return gitobjects.ObjectTypeCommit, &fakeObject{hash: hash8}, nil
// 	}

// 	return "", nil, nil
// }

// func sortObjects(objects []walkerStreamItem) []walkerStreamItem {
// 	sort.Slice(objects, func(i, j int) bool {
// 		return objects[i].hash.String() < objects[j].hash.String()
// 	})

// 	return objects
// }

// func TestWalkerEmptyMask(t *testing.T) {
// 	require := require.New(t)

// 	var mask remote.ReachableObjectsMask

// 	w := newWalker(hash1, mask, decoder)
// 	ch, err := w.stream()
// 	require.NoError(err, "creating stream")
// 	got := []walkerStreamItem{}

// 	for item := range ch {
// 		got = append(got, item)
// 	}

// 	want := []walkerStreamItem{
// 		{
// 			hash:    hash1,
// 			objType: gitobjects.ObjectTypeCommit,
// 		},
// 	}

// 	require.Equal(want, got)
// }

// func TestWalkerOnlyCommits(t *testing.T) {
// 	require := require.New(t)

// 	var mask remote.ReachableObjectsMask
// 	mask.SetCommitMask()

// 	w := newWalker(hash1, mask, decoder)
// 	ch, err := w.stream()
// 	require.NoError(err, "creating stream")
// 	got := []walkerStreamItem{}

// 	for item := range ch {
// 		got = append(got, item)
// 	}

// 	want := []walkerStreamItem{
// 		{
// 			hash:    hash1,
// 			objType: gitobjects.ObjectTypeCommit,
// 		},
// 		{
// 			hash:    hash2,
// 			objType: gitobjects.ObjectTypeCommit,
// 		},
// 	}

// 	require.Equal(sortObjects(want), sortObjects(got))
// }

// func TestWalkerOnlyTrees(t *testing.T) {
// 	require := require.New(t)

// 	var mask remote.ReachableObjectsMask
// 	mask.SetTreeMask()

// 	w := newWalker(hash1, mask, decoder)
// 	ch, err := w.stream()
// 	require.NoError(err, "creating stream")
// 	got := []walkerStreamItem{}

// 	for item := range ch {
// 		got = append(got, item)
// 	}

// 	want := []walkerStreamItem{
// 		{
// 			hash:    hash1,
// 			objType: gitobjects.ObjectTypeCommit,
// 		},
// 		{
// 			hash:    hash3,
// 			objType: gitobjects.ObjectTypeDir,
// 		},
// 		{
// 			hash:    hash6,
// 			objType: gitobjects.ObjectTypeDir,
// 		},
// 	}

// 	require.Equal(sortObjects(want), sortObjects(got))
// }

// func TestWalkerAll(t *testing.T) {
// 	require := require.New(t)

// 	var mask remote.ReachableObjectsMask
// 	mask.SetTreeMask().SetBlobMask().SetCommitMask()

// 	w := newWalker(hash1, mask, decoder)
// 	ch, err := w.stream()
// 	require.NoError(err, "creating stream")
// 	got := []walkerStreamItem{}

// 	for item := range ch {
// 		got = append(got, item)
// 	}

// 	want := []walkerStreamItem{
// 		{
// 			hash:    hash1,
// 			objType: gitobjects.ObjectTypeCommit,
// 		},
// 		{
// 			hash:    hash2,
// 			objType: gitobjects.ObjectTypeCommit,
// 		},
// 		{
// 			hash:    hash3,
// 			objType: gitobjects.ObjectTypeDir,
// 		},
// 		{
// 			hash:    hash4,
// 			objType: gitobjects.ObjectTypeDir,
// 		},
// 		{
// 			hash:    hash5,
// 			objType: binding.ObjectTypeBlob,
// 		},
// 		{
// 			hash:    hash6,
// 			objType: gitobjects.ObjectTypeDir,
// 		},
// 		{
// 			hash:    hash7,
// 			objType: binding.ObjectTypeBlob,
// 		},
// 		{
// 			hash:    hash8,
// 			objType: binding.ObjectTypeBlob,
// 		},
// 	}

// 	require.Equal(sortObjects(want), sortObjects(got))
// }
