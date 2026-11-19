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

package server

import (
	"math"
	"slices"

	"github.com/google/abfs/shared/cacheman/protocol"
)

// buildRunTypes are the run types of the ninja log (ninjago/ninjalog) that are reported by
// name in the run_type label.
var buildRunTypes = []string{
	"local",
	"local-no-tap",
	"fallback-busy",
	"fallback-error",
	"remote",
	"cached",
	"delayed-cached",
}

// buildMissReasons are the reasons a step was executed (binding.CacheReason*) that are
// reported by name in the reason label.
var buildMissReasons = []string{
	"cache_off",
	"no_result",
	"skipped",
	"mismatch",
	"apply_failed",
}

// buildRejectReasons are the reasons a cached response was not used that are reported by
// name in the reason label.
var buildRejectReasons = []string{
	"skipped",
	"mismatch",
	"apply_failed",
}

// boundedLabel returns v when it is one of known, and "other" otherwise, so that a label
// fed by a client stays bounded.
func boundedLabel(v string, known []string) string {
	if slices.Contains(known, v) {
		return v
	}
	return "other"
}

// validSeconds reports whether v can be added to a counter.
func validSeconds(v float64) bool {
	return v >= 0 && !math.IsInf(v, 0) && !math.IsNaN(v)
}

func (c *CacheMan) recordBuildSteps(req *protocol.PushBuildMetricsReq) {
	steps := req.Steps
	labels := []string{req.BuildGoal, req.Status, req.AndroidTarget}

	for runType, n := range steps.RunTypes {
		c.buildStepsTotal.Add(float64(n), append(labels, boundedLabel(runType, buildRunTypes))...)
	}
	for reason, n := range steps.MissReasons {
		c.buildMissReasonsTotal.Add(float64(n), append(labels, boundedLabel(reason, buildMissReasons))...)
	}
	for reason, n := range steps.RejectedResponses {
		c.buildRejectedResponsesTotal.Add(float64(n), append(labels, boundedLabel(reason, buildRejectReasons))...)
	}

	if !validSeconds(steps.CacheHitRunSecs) || !validSeconds(steps.CacheHitCPUSecs) || !validSeconds(steps.ExecutedRunSecs) {
		return
	}
	c.buildStepRunSecondsTotal.Add(steps.CacheHitRunSecs, append(labels, "cache")...)
	c.buildStepRunSecondsTotal.Add(steps.ExecutedRunSecs, append(labels, "executed")...)
	c.buildCacheSavedCPUSecondsTotal.Add(steps.CacheHitCPUSecs, labels...)
	if total := steps.CacheHitRunSecs + steps.ExecutedRunSecs; total > 0 {
		c.buildCacheTimeHitRatioHist.Observe(steps.CacheHitRunSecs/total, labels...)
	}
}
