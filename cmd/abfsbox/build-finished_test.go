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

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cacheman/protocol"
)

func TestBuildStepStats(t *testing.T) {
	require.Nil(t, buildStepStats(nil))

	got := buildStepStats(&binding.StepStats{
		RunTypes:           map[string]uint32{"cached": 2},
		MissReasons:        map[string]uint32{"no_result": 1},
		NotLookedUp:        map[string]uint32{"cache_off": 4},
		RejectedResponses:  map[string]uint32{"skipped": 3},
		CacheHitRunSecs:    1.5,
		CacheHitCPUSecs:    2.5,
		ExecutedRunSecs:    3.5,
		NotLookedUpRunSecs: 4.5,
	})
	require.Equal(t, &protocol.BuildStepStats{
		RunTypes:           map[string]uint32{"cached": 2},
		MissReasons:        map[string]uint32{"no_result": 1},
		NotLookedUp:        map[string]uint32{"cache_off": 4},
		RejectedResponses:  map[string]uint32{"skipped": 3},
		CacheHitRunSecs:    1.5,
		CacheHitCPUSecs:    2.5,
		ExecutedRunSecs:    3.5,
		NotLookedUpRunSecs: 4.5,
	}, got)
}

func TestIsZeroWork(t *testing.T) {
	tests := []struct {
		name  string
		stats binding.NinjaStats
		want  bool
	}{
		{name: "no step", stats: binding.NinjaStats{Steps: &binding.StepStats{}}, want: true},
		{name: "stats file without steps", stats: binding.NinjaStats{}, want: true},
		{name: "cache hit", stats: binding.NinjaStats{CacheHitCount: 1}},
		{name: "cache miss", stats: binding.NinjaStats{CacheMissCount: 1}},
		{
			name:  "cache off",
			stats: binding.NinjaStats{Steps: &binding.StepStats{NotLookedUp: map[string]uint32{"cache_off": 2}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isZeroWork(&tt.stats))
		})
	}
}
