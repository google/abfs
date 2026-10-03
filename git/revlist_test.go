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

package git

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestParseRevListOutput_Empty(t *testing.T) {
	require := require.New(t)
	r, err := parseRevListObjectsOutput(strings.NewReader(""))
	require.NoError(err)
	require.Empty(r.Blobs)
	require.Empty(r.Objects)
}

func TestParseRevListOutput_AllObjects(t *testing.T) {
	require := require.New(t)
	hash1 := entities.NewHash("5898b72344c8eb0e0e88b2419d775864be6bfa84")
	hash2 := entities.NewHash("dd464f319fb6e82f981e26fed3b76482f15bd746")
	hash3 := entities.NewHash("c3c8d20dc1b302a3ac16fe62c3302f1d0c5b6182")
	fakeInput := fmt.Sprintf("%s\n%s\n%s\n", hash1, hash2, hash3)
	r, err := parseRevListObjectsOutput(strings.NewReader(fakeInput))
	require.NoError(err)
	require.Empty(r.Blobs)
	require.Equal([]entities.Hash{hash1, hash2, hash3}, r.Objects)
}

func TestParseRevListOutput_AllBlobs(t *testing.T) {
	require := require.New(t)
	hash1 := entities.NewHash("5898b72344c8eb0e0e88b2419d775864be6bfa84")
	hash2 := entities.NewHash("dd464f319fb6e82f981e26fed3b76482f15bd746")
	hash3 := entities.NewHash("c3c8d20dc1b302a3ac16fe62c3302f1d0c5b6182")
	fakeInput := fmt.Sprintf("~%s\n~%s\n~%s\n", hash1, hash2, hash3)
	r, err := parseRevListObjectsOutput(strings.NewReader(fakeInput))
	require.NoError(err)
	require.Empty(r.Objects)
	require.Equal([]entities.Hash{hash1, hash2, hash3}, r.Blobs)
}

func TestParseRevListOutput_Mix(t *testing.T) {
	require := require.New(t)
	hash1 := entities.NewHash("5898b72344c8eb0e0e88b2419d775864be6bfa84")
	hash2 := entities.NewHash("dd464f319fb6e82f981e26fed3b76482f15bd746")
	hash3 := entities.NewHash("c3c8d20dc1b302a3ac16fe62c3302f1d0c5b6182")
	fakeInput := fmt.Sprintf("%s\n~%s\n%s\n", hash1, hash2, hash3)
	r, err := parseRevListObjectsOutput(strings.NewReader(fakeInput))
	require.NoError(err)
	require.Equal([]entities.Hash{hash1, hash3}, r.Objects)
	require.Equal([]entities.Hash{hash2}, r.Blobs)
}
