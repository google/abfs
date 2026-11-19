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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago/ninjalog"
)

func TestMissReason(t *testing.T) {
	tests := []struct {
		name     string
		cacheOff bool
		eval     Eval
		want     string
	}{
		{name: "no response", want: binding.CacheReasonNoResult},
		{name: "cache off", cacheOff: true, eval: Eval{mismatched: 1}, want: binding.CacheReasonOff},
		{name: "skipped", eval: Eval{skipped: 2}, want: binding.CacheReasonSkipped},
		{name: "mismatch", eval: Eval{mismatched: 1}, want: binding.CacheReasonMismatch},
		{name: "apply failed", eval: Eval{applyFailed: 1}, want: binding.CacheReasonApplyFailed},
		{name: "mismatch and skipped", eval: Eval{skipped: 3, mismatched: 1}, want: binding.CacheReasonMismatch},
		{name: "apply failed and skipped", eval: Eval{skipped: 3, applyFailed: 1}, want: binding.CacheReasonApplyFailed},
		{name: "mismatch and apply failed", eval: Eval{mismatched: 1, applyFailed: 1}, want: binding.CacheReasonMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, missReason(tt.cacheOff, &tt.eval))
		})
	}
}

func evalResponse(runSecs, userSecs, sysSecs float64) *binding.EvalResponse {
	resp := new(binding.EvalResponse)
	resp.RunInfo.RunDuration = runSecs
	resp.RunInfo.UserTime = userSecs
	resp.RunInfo.SysTime = sysSecs
	return resp
}

func TestStepCounter(t *testing.T) {
	var s stepCounter
	require.Equal(t, &binding.StepStats{}, s.snapshot())

	eval := &Eval{}
	s.rejectResponse(eval, binding.CacheReasonSkipped)
	s.rejectResponse(eval, binding.CacheReasonSkipped)
	s.rejectResponse(eval, binding.CacheReasonMismatch)
	s.rejectResponse(eval, binding.CacheReasonApplyFailed)
	require.Equal(t, 2, eval.skipped)
	require.Equal(t, 1, eval.mismatched)
	require.Equal(t, 1, eval.applyFailed)

	s.addStep(ninjalog.RunTypeCached, "", evalResponse(10, 6, 2))
	s.addStep(ninjalog.RunTypeDelayedCached, "", evalResponse(5, 3, 1))
	s.addStep(ninjalog.RunTypeLocal, binding.CacheReasonMismatch, evalResponse(4, 1, 1))
	s.addStep(ninjalog.RunTypeRemote, binding.CacheReasonNoResult, evalResponse(1, 0, 0))

	got := s.snapshot()
	require.Equal(t, &binding.StepStats{
		RunTypes: map[string]uint32{"cached": 1, "delayed-cached": 1, "local": 1, "remote": 1},
		MissReasons: map[string]uint32{
			binding.CacheReasonMismatch: 1,
			binding.CacheReasonNoResult: 1,
		},
		RejectedResponses: map[string]uint32{
			binding.CacheReasonSkipped:     2,
			binding.CacheReasonMismatch:    1,
			binding.CacheReasonApplyFailed: 1,
		},
		CacheHitRunSecs: 15,
		CacheHitCPUSecs: 12,
		ExecutedRunSecs: 5,
	}, got)

	// The snapshot does not share maps with the counter.
	s.addStep(ninjalog.RunTypeCached, "", evalResponse(1, 0, 0))
	require.Equal(t, uint32(1), got.RunTypes["cached"])
}

func TestNinjaStatsSteps_JSON(t *testing.T) {
	// Stats files written before steps were counted decode with nil Steps.
	var old binding.NinjaStats
	require.NoError(t, json.Unmarshal([]byte(`{"CacheHitCount":3,"CacheMissCount":1}`), &old))
	require.Nil(t, old.Steps)

	// A run that counted no step still writes Steps, so readers can tell it apart.
	b, err := json.Marshal(binding.NinjaStats{Steps: new(stepCounter).snapshot()})
	require.NoError(t, err)
	var stats binding.NinjaStats
	require.NoError(t, json.Unmarshal(b, &stats))
	require.Equal(t, &binding.StepStats{}, stats.Steps)
}
