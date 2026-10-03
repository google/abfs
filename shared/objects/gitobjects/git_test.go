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

package gitobjects

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestDeepTree_Empty(t *testing.T) {
	require := require.New(t)

	dp := &DeepTreeObject{}
	err := dp.Encode()
	require.NoError(err)

	require.Equal(string(dp.Encoded), "deeptree 0\x00")
}

func TestDeepTree_Simple(t *testing.T) {
	require := require.New(t)

	dp := &DeepTreeObject{
		GitBaseObject: GitBaseObject{},
		DeepTree: entities.DeepTree{Entries: []*entities.DeepEntry{
			{
				Name: "file-1",
				Mode: entities.FileModeRegular,
				Hash: entities.EmptyFileHash,
				Size: 0,
			},
			{
				Name: "dir-1",
				Mode: entities.FileModeDir,
				Hash: entities.EmptyDirHash,
				Size: 123,
			},
		}},
	}
	err := dp.Encode()
	require.NoError(err)

	dp2 := &DeepTreeObject{}
	require.NoError(dp2.Decode(dp.Hash, dp.Encoded))
	require.Equal(dp, dp2)
}
