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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestEvalRespCoding(t *testing.T) {
	runTime, err := time.Parse(time.RFC3339Nano, "2023-01-26T20:27:02.801596356-08:00")
	require.NoError(t, err)

	resp := &EvalResponse{
		Version:     "0.1",
		EvalRequest: entities.NewHash("1d34c0cc495a4d40e52243b0db70800a9f7314eb"),
		Stdout:      "out out",
		Stderr:      "err err",
		ExitCode:    1,
		RunInfo: struct {
			Pid                 int
			Timestamp           time.Time
			BuildStartHash      Hash
			RunDuration         float64
			SnapshotDuration    float64
			PostProcessDuration float64
			UserTime            float64
			SysTime             float64
			Memory              int64
			TapCounts           map[string]uint64 `json:",omitempty"`
			IO                  struct {
				In  int64
				Out int64
			}
			Misc struct {
				SoftFault           int64
				HardFault           int64
				VolContextSwitch    int64
				ForcedContextSwitch int64
			}
		}{
			Timestamp:           runTime,
			BuildStartHash:      entities.EmptyFileHash,
			RunDuration:         10.5,
			SnapshotDuration:    1.1,
			PostProcessDuration: 1.2,
		},
		ReadPatch: []*entities.PatchEntry{
			{
				Path:  "a/p/c",
				Mode:  0120000,
				Hash:  entities.NewHash("a385a087aee79c5b052d5c75584ac505c4cc1a6b"),
				Size:  123,
				State: entities.PatchEntryStateIn,
			},
		},
		WritePatch: []*entities.PatchEntry{},
		Deletes: []string{
			"a/b/extra-file",
		},
	}

	err = resp.Encode()
	require.NoError(t, err)

	json := `{"Version":"0.1","EvalRequest":"1d34c0cc495a4d40e52243b0db70800a9f7314eb","Stdout":"out out","Stderr":"err err","ExitCode":1,"RunInfo":{"Pid":0,"Timestamp":"2023-01-26T20:27:02.801596356-08:00","BuildStartHash":"e69de29bb2d1d6434b8b29ae775ad8c2e48c5391","RunDuration":10.5,"SnapshotDuration":1.1,"PostProcessDuration":1.2,"UserTime":0,"SysTime":0,"Memory":0,"IO":{"In":0,"Out":0},"Misc":{"SoftFault":0,"HardFault":0,"VolContextSwitch":0,"ForcedContextSwitch":0}},"ReadPatch":[["a/p/c","s","a385a087aee79c5b052d5c75584ac505c4cc1a6b","123","3"]],"WritePatch":[],"Deletes":["a/b/extra-file"]}`

	assert.Equal(t, fmt.Sprintf("eval-response %d\x00%s", len(json), json), string(resp.Encoded))

	resp2 := &EvalResponse{}

	err = resp2.Decode(resp.Hash, resp.Encoded)
	require.NoError(t, err)
	assert.Equal(t, resp, resp2)
}
