// Copyright 2025 Google LLC
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

package cas

import (
	"fmt"
	"testing"

	"github.com/google/abfs/cas/hash"
	"github.com/stretchr/testify/require"
)

func testCreateDeepDir(ts *test_store, depth int) (hash.Hash, error) {
	var createDir func(level int) (hash.Hash, error)

	createDir = func(level int) (hash.Hash, error) {
		targetFileName := fmt.Sprintf("target-%d", level)
		level--
		taEntry := DirEntry{Hash: hashFile(targetFileName, 300), Size: 300, Mode: FileModeFile(), Name: targetFileName}
		if level == 0 {
			return ts.SaveDirEntries(NewDirInMemory([]DirEntry{taEntry}))
		}
		ch, err := createDir(level)
		if err != nil {
			return nil, err
		}
		des := []DirEntry{taEntry}
		des = append(des, DirEntry{Hash: ch, Size: 0, Mode: FileModeDir(), Name: "dir"})
		return ts.SaveDirEntries(NewDirInMemory(des))

	}
	return createDir(depth)
}
func TestDeepTree_Create(t *testing.T) {
	ts := newTestStore()
	sz := 31
	h, err := testCreateDeepDir(ts, sz)
	require.NoError(t, err)
	dtH, err := CreateDeepTree(h, ts, 10)
	require.NoError(t, err)
	walkDeepTree(t, dtH, ts, sz)
}
