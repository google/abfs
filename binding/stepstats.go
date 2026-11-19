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
	// CacheReasonOff means the cache lookup is turned off.
	CacheReasonOff = "cache_off"
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
	// MissReasons counts the steps that were executed, by cache reason.
	MissReasons map[string]uint32 `json:",omitempty"`
	// RejectedResponses counts the cached responses that were not used, by cache reason
	// (skipped, mismatch or apply_failed).
	RejectedResponses map[string]uint32 `json:",omitempty"`
	// CacheHitRunSecs and CacheHitCPUSecs sum the run time and the user and system CPU
	// time of the steps taken from the cache, as measured by the builds that produced them.
	CacheHitRunSecs float64
	CacheHitCPUSecs float64
	// ExecutedRunSecs sums the run time of the steps executed in this run.
	ExecutedRunSecs float64
}
