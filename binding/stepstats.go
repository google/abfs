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

// Reasons why a cached response was rejected, or why a step was executed instead of
// taken from the cache.
const (
	// CacheReasonOff means the cache lookup is turned off, so the step was not looked up.
	CacheReasonOff = "cache_off"
	// CacheReasonPhonyOutput means the step declares phony_output, so it runs on every
	// build and is never looked up.
	CacheReasonPhonyOutput = "phony_output"
	// CacheReasonNoResult means the cache held no response for the step's request.
	CacheReasonNoResult = "no_result"
	// CacheReasonSkipped means a cached response does not write the step's targets.
	CacheReasonSkipped = "skipped"
	// CacheReasonMismatch means the inputs of a cached response did not match.
	CacheReasonMismatch = "mismatch"
	// CacheReasonApplyFailed means a cached response failed to apply with an error other
	// than a mismatch.
	CacheReasonApplyFailed = "apply_failed"
)

// StepStats counts the steps of a ninja run and sums their run times.
type StepStats struct {
	// RunTypes counts the steps that completed, by ninja log run type.
	RunTypes map[string]uint32 `json:",omitempty"`
	// MissReasons counts the steps that were executed because their cache lookup found no
	// usable response, by cache reason.
	MissReasons map[string]uint32 `json:",omitempty"`
	// NotLookedUp counts the steps that were executed without a cache lookup, by cache
	// reason.
	NotLookedUp map[string]uint32 `json:",omitempty"`
	// RejectedResponses counts the cached responses that were not used, by cache reason
	// (skipped, mismatch or apply_failed).
	RejectedResponses map[string]uint32 `json:",omitempty"`
	// CacheHitRunSecs and CacheHitCPUSecs sum the run time and the user and system CPU
	// time of the steps taken from the cache, as measured by the builds that produced them.
	CacheHitRunSecs float64
	CacheHitCPUSecs float64
	// ExecutedRunSecs sums the run time of the steps executed after a cache miss.
	ExecutedRunSecs float64
	// NotLookedUpRunSecs sums the run time of the steps executed without a cache lookup.
	NotLookedUpRunSecs float64
}

// CacheCounts returns the steps taken from the cache, the executed steps whose lookup found
// no usable response, and the executed steps that were not looked up.
func (s *StepStats) CacheCounts() (hits, misses, notLookedUp uint32) {
	var completed uint32
	for _, n := range s.RunTypes {
		completed += n
	}
	for _, n := range s.MissReasons {
		misses += n
	}
	for _, n := range s.NotLookedUp {
		notLookedUp += n
	}
	// Every completed step that is not a cache hit counts once in MissReasons or
	// NotLookedUp. Failed steps are not counted at all.
	if completed > misses+notLookedUp {
		hits = completed - misses - notLookedUp
	}
	return hits, misses, notLookedUp
}
