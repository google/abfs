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
	"maps"
	"sync"

	"github.com/google/ninja-go/build"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago/ninjalog"
)

// stepCounter collects the step counts of a ninja run for the ninja stats file. The zero
// value is ready to use.
type stepCounter struct {
	mu    sync.Mutex
	stats binding.StepStats
}

// rejectResponse counts a cached response that was not used, and records the reason in
// the step's Eval.
func (s *stepCounter) rejectResponse(eval *Eval, reason string) {
	switch reason {
	case binding.CacheReasonSkipped:
		eval.skipped++
	case binding.CacheReasonMismatch:
		eval.mismatched++
	default:
		eval.applyFailed++
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stats.RejectedResponses == nil {
		s.stats.RejectedResponses = make(map[string]uint32)
	}
	s.stats.RejectedResponses[reason]++
}

// addStep counts a completed step. missReason and lookedUp are ignored for cache hits.
func (s *stepCounter) addStep(runType ninjalog.RunType, missReason string, lookedUp bool, resp *binding.EvalResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stats.RunTypes == nil {
		s.stats.RunTypes = make(map[string]uint32)
	}
	s.stats.RunTypes[string(runType)]++
	if runType.IsCacheHit() {
		s.stats.CacheHitRunSecs += resp.RunInfo.RunDuration
		s.stats.CacheHitCPUSecs += resp.RunInfo.UserTime + resp.RunInfo.SysTime
		return
	}
	if !lookedUp {
		if s.stats.NotLookedUp == nil {
			s.stats.NotLookedUp = make(map[string]uint32)
		}
		s.stats.NotLookedUp[missReason]++
		s.stats.NotLookedUpRunSecs += resp.RunInfo.RunDuration
		return
	}
	if s.stats.MissReasons == nil {
		s.stats.MissReasons = make(map[string]uint32)
	}
	s.stats.MissReasons[missReason]++
	s.stats.ExecutedRunSecs += resp.RunInfo.RunDuration
}

func (s *stepCounter) snapshot() *binding.StepStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := s.stats
	stats.RunTypes = maps.Clone(s.stats.RunTypes)
	stats.MissReasons = maps.Clone(s.stats.MissReasons)
	stats.NotLookedUp = maps.Clone(s.stats.NotLookedUp)
	stats.RejectedResponses = maps.Clone(s.stats.RejectedResponses)
	return &stats
}

// missReason tells why a step is executed instead of taken from the cache, and whether
// the step was looked up. A phony_output step is never looked up, even with the cache on, so
// it ranks first. A mismatch outranks an apply failure, which outranks a skipped response.
func missReason(cacheOff bool, eval *Eval) (reason string, lookedUp bool) {
	switch {
	case eval.step != nil && eval.step.IsPhonyOutput():
		return binding.CacheReasonPhonyOutput, false
	case cacheOff:
		return binding.CacheReasonOff, false
	case eval.mismatched > 0:
		return binding.CacheReasonMismatch, true
	case eval.applyFailed > 0:
		return binding.CacheReasonApplyFailed, true
	case eval.skipped > 0:
		return binding.CacheReasonSkipped, true
	default:
		return binding.CacheReasonNoResult, true
	}
}

// cacheCounts returns the cache hits, misses and steps not looked up of the run. A dry run
// looks up no step, so every step it would run counts as not looked up.
func (c *ABFSBackend) cacheCounts(steps *binding.StepStats) (hits, misses, notLookedUp uint32) {
	if c.dryRun {
		return 0, 0, uint32(c.engine.StateCounts()[build.StateDone])
	}
	return steps.CacheCounts()
}
