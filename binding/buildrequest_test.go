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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestBuildResult01Decode(t *testing.T) {
	require := require.New(t)
	oldType := &OldBuildResult01{
		Version:        OldBuildResult01Version,
		BuildRequest:   entities.NewHash("70cd98c639c51a0b4948743eef3739668d5742f2"),
		MountDetails:   entities.NewHash("97eea1a1e2908e8fa7ce048cde163766660628df"),
		BuildInfos:     []Hash{entities.NewHash("a4ecff1d8cdacc35bcdb8781891b4a03073808b2")},
		UnmountDetails: entities.NewHash("5baa533b63cedd95ee74334104a073e078a9d32a"),
		Err:            "",
	}

	err := ObjectJSONEncode(oldType, ObjectTypeBuildResult)
	require.NoError(err, "shouldn't be an error from encoding old object")
	require.Equal(entities.NewHash("a6a2698a2dd26c13a4d2ea6d430569639629755c"), oldType.Hash)

	fmt.Printf("oldType content %v\n", string(oldType.Encoded))

	newType := &BuildResult{}
	err = newType.Decode(oldType.Hash, oldType.Encoded)
	require.NoError(err, "shouldn't be an error decoding to new object")

	require.Equal(CurrentBuildResultVersion, newType.Version)
	require.Equal(oldType.BuildRequest, newType.BuildRequest)
	require.Equal(oldType.MountDetails, newType.MountDetails)
	require.Len(newType.BuildHashes, 1)
	require.Equal(oldType.BuildInfos[0], newType.BuildHashes[0].BuildInfo)
	require.Equal(oldType.UnmountDetails, newType.UnmountDetails)
	require.Equal(oldType.Err, newType.Err)
}
