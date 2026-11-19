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
		RunTypes:          map[string]uint32{"cached": 2},
		MissReasons:       map[string]uint32{"no_result": 1},
		RejectedResponses: map[string]uint32{"skipped": 3},
		CacheHitRunSecs:   1.5,
		CacheHitCPUSecs:   2.5,
		ExecutedRunSecs:   3.5,
	})
	require.Equal(t, &protocol.BuildStepStats{
		RunTypes:          map[string]uint32{"cached": 2},
		MissReasons:       map[string]uint32{"no_result": 1},
		RejectedResponses: map[string]uint32{"skipped": 3},
		CacheHitRunSecs:   1.5,
		CacheHitCPUSecs:   2.5,
		ExecutedRunSecs:   3.5,
	}, got)
}
