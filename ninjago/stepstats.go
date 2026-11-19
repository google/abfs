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

// addStep counts a completed step. missReason is ignored for cache hits.
func (s *stepCounter) addStep(runType ninjalog.RunType, missReason string, resp *binding.EvalResponse) {
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
	stats.RejectedResponses = maps.Clone(s.stats.RejectedResponses)
	return &stats
}

// missReason tells why a step whose cache lookup found no usable response is executed.
// A mismatch outranks an apply failure, which outranks a skipped response.
func missReason(cacheOff bool, eval *Eval) string {
	switch {
	case cacheOff:
		return binding.CacheReasonOff
	case eval.mismatched > 0:
		return binding.CacheReasonMismatch
	case eval.applyFailed > 0:
		return binding.CacheReasonApplyFailed
	case eval.skipped > 0:
		return binding.CacheReasonSkipped
	default:
		return binding.CacheReasonNoResult
	}
}
