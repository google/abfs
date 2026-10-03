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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestPatchSimpleEncode(t *testing.T) {
	require := require.New(t)
	patch := &PatchObject{
		Version: CurrentPatchVersion,
		Patch: []*entities.PatchEntry{
			{
				Path:  "a/b/c",
				Mode:  0120000,
				Hash:  entities.NewHash("a385a087aee79c5b052d5c75584ac505c4cc1a6b"),
				Size:  123,
				State: entities.PatchEntryStateIn,
			},
		},
	}

	require.NoError(patch.Encode(), "expected no error during Encode()")

	patch2 := &PatchObject{}

	require.NoError(patch2.Decode(patch.Hash, patch.Encoded), "expected no error during Decode()")

	require.Equal(patch, patch2, "expected original patch and decoded patch to be equal")
}

func TestPatchLinks(t *testing.T) {
	require := require.New(t)
	patch := &PatchObject{
		Version: CurrentPatchVersion,
		Patch: []*entities.PatchEntry{
			{
				Path: "a/b/c",
				Mode: 0120000,
				Hash: entities.NewHash("a385a087aee79c5b052d5c75584ac505c4cc1a6b"),
				Size: 123,
			},
			{
				Path: "d/e/f",
				Mode: 0120000,
				Hash: entities.ZeroHash,
				Size: 0,
			},
			{
				Path: "g/h/i",
				Mode: 0120000,
				Hash: entities.EmptyFileHash,
				Size: 0,
			},
		},
	}

	links := patch.Links()

	require.Len(links, 2, "expected 2 links because the zero hash should be excluded")

	require.Contains(links, entities.NewHash("a385a087aee79c5b052d5c75584ac505c4cc1a6b"))
	require.Contains(links, entities.EmptyFileHash)
}
