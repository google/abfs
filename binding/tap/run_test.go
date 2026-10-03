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

package tap

import (
	"fmt"
	"io/fs"
	"math/rand"
	"testing"
	"time"

	"github.com/google/abfs/shared/entities"
)

// We previously had a data race bug in updateResult where directories were not
// calling lock.Lock() before appending to the tapResult. This test showed the
// data race when run with -race
func TestUpdateResultDataRace(t *testing.T) {
	snap := &snapshot{
		prefix: "",
		hashStat: func(path string) (*entities.HashStat, error) {
			// Introduce jitter to maximize chances of concurrent execution overlap
			time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)
			return &entities.HashStat{
				Hash: entities.NewHash("da39a3ee5e6b4b0d3255bfef95601890afd80709"),
				Mode: entities.FileModeRegular,
				Size: 10,
			}, nil
		},
	}
	s := newTapsPatch(snap, false, "")

	// Populate the tracker with many directories and files to trigger a race.
	// We want a mix of directories (handled synchronously in WalkFunc)
	// and files (handled concurrently in goroutines) to run at the same time.
	for i := 0; i < 200; i++ {
		dirPath := fmt.Sprintf("dir-%d", i)
		s.tracker.Add(dirPath, fs.ModeDir|0755)
		for j := 0; j < 5; j++ {
			filePath := fmt.Sprintf("dir-%d/file-%d", i, j)
			s.tracker.Add(filePath, 0644)
		}
	}

	tapResult := &entities.TapCmdResult{}

	// Run updateResult, which walks the tracker and populates WritePatch
	err := updateResult(tapResult, s, snap, 1)
	if err != nil {
		t.Fatalf("updateResult failed: %v", err)
	}
}
