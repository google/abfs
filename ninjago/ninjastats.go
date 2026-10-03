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

package ninjago

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/ninja-go/build"

	"github.com/google/abfs/binding"
)

const ninjaStatsFile = ".ninja_stats"

func stats(c *ABFSBackend, err error) binding.NinjaStats {
	states := c.engine.StateCounts()
	s := binding.NinjaStats{
		GoalTargets:     c.graph.GoalTargets,
		StartTime:       c.startTime,
		RunSecs:         time.Since(c.startTime).Seconds(),
		GraphNodesCount: c.graph.Nodes.Len(),
		GraphFilesCount: c.graph.Files.Len(),
		NodesCount:      uint32(c.graph.Nodes.Len()),
		DoneCount:       uint32(states[build.StateDone] + states[build.StateCached]),
		FailCount:       uint32(states[build.StateFailed]),
		CacheHitCount:   uint32(states[build.StateCached]),
		CacheMissCount:  uint32(states[build.StateDone]),
		NinjaEntryFile:  c.ninjaEntryFile,
		BuildFiles:      c.ninjaFiles,
	}
	if err != nil {
		s.Error = err.Error()
	}

	total := s.CacheMissCount + s.CacheHitCount + s.CacheForceCount
	s.CacheHitPercent = 100.0 * float64(s.CacheHitCount) / float64(max(total, 1))

	s.Failures = make([]binding.NinjaFailure, len(c.faileds))
	for i, failed := range c.faileds {
		s.Failures[i] = binding.NinjaFailure{
			Targets: failed.step.Node.Targets,
			Error:   failed.err.Error(),
		}
		if failed.req != nil {
			s.Failures[i].Req = failed.req.Hash.String()
		}
		if failed.resp != nil {
			s.Failures[i].Resp = failed.resp.Hash.String()
		}
	}

	return s
}

func writeStats(c *ABFSBackend, inErr error) (err error) {
	defer func() {
		if inErr != nil {
			if err != nil {
				c.log.Errorf("Finalizing error: %s", err)
			}
			err = inErr
		}
	}()
	s := stats(c, inErr)

	var f *os.File
	for i := 0; ; i++ {
		name := fmt.Sprintf("%s.%d", filepath.Join(c.graph.BuildDir, ninjaStatsFile), i)
		f, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		break
	}

	if err := json.NewEncoder(f).Encode(s); err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	return nil
}
