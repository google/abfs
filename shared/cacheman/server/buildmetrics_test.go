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
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago/ninjalog"
	"github.com/google/abfs/shared/cacheman/protocol"
)

// fakeMetric records counter adds, gauge sets and histogram observations by joined label
// values.
type fakeMetric struct {
	mu     sync.Mutex
	values map[string][]float64
}

func newFakeMetric() *fakeMetric {
	return &fakeMetric{values: make(map[string][]float64)}
}

func (f *fakeMetric) record(val float64, labelValues []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.Join(labelValues, ",")
	f.values[key] = append(f.values[key], val)
}

func (f *fakeMetric) Inc(labelValues ...string)              { f.record(1, labelValues) }
func (f *fakeMetric) Add(val float64, labelValues ...string) { f.record(val, labelValues) }
func (f *fakeMetric) Set(val float64, labelValues ...string) { f.record(val, labelValues) }
func (f *fakeMetric) Observe(val float64, labelValues ...string) {
	f.record(val, labelValues)
}

func (f *fakeMetric) get() map[string][]float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make(map[string][]float64, len(f.values))
	for k, v := range f.values {
		values[k] = append([]float64(nil), v...)
	}
	return values
}

type buildMetricsFakes struct {
	duration, hitRatio, phases, hits, misses       *fakeMetric
	steps, missReasons, rejected, runSecs, cpuSecs *fakeMetric
	timeHitRatio                                   *fakeMetric
}

func newBuildMetricsCacheMan() (*CacheMan, *buildMetricsFakes) {
	f := &buildMetricsFakes{
		duration:     newFakeMetric(),
		hitRatio:     newFakeMetric(),
		phases:       newFakeMetric(),
		hits:         newFakeMetric(),
		misses:       newFakeMetric(),
		steps:        newFakeMetric(),
		missReasons:  newFakeMetric(),
		rejected:     newFakeMetric(),
		runSecs:      newFakeMetric(),
		cpuSecs:      newFakeMetric(),
		timeHitRatio: newFakeMetric(),
	}
	c := &CacheMan{
		printer:                        new(DefaultPrinter),
		buildDurationHist:              f.duration,
		buildCacheHitRatioHist:         f.hitRatio,
		buildPhaseDurationHist:         f.phases,
		buildCacheHitsTotal:            f.hits,
		buildCacheMissesTotal:          f.misses,
		buildStepsTotal:                f.steps,
		buildMissReasonsTotal:          f.missReasons,
		buildRejectedResponsesTotal:    f.rejected,
		buildStepRunSecondsTotal:       f.runSecs,
		buildCacheSavedCPUSecondsTotal: f.cpuSecs,
		buildCacheTimeHitRatioHist:     f.timeHitRatio,
	}
	return c, f
}

func pushBuildMetrics(t *testing.T, c *CacheMan, body string) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/push-build-metrics", strings.NewReader(body))
	c.handlePushBuildMetrics(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// oldBuildMetricsReq is a request as an abfsbox without step counts sends it.
const oldBuildMetricsReq = `{
	"AndroidTarget": "aosp_cf_x86_64_phone-trunk_staging-userdebug",
	"BuildGoal": "droid",
	"Status": "success",
	"BuildDurationSecs": 120,
	"HasNinjaRun": true,
	"CacheHitRatio": 0.75,
	"CacheHitCount": 3,
	"CacheMissCount": 1,
	"Phases": {"run": 100}
}`

func TestPushBuildMetrics_OldRequest(t *testing.T) {
	c, f := newBuildMetricsCacheMan()
	pushBuildMetrics(t, c, oldBuildMetricsReq)

	labels := "droid,success,aosp_cf_x86_64_phone-trunk_staging-userdebug"
	require.Equal(t, map[string][]float64{labels: {120}}, f.duration.get())
	require.Equal(t, map[string][]float64{labels: {0.75}}, f.hitRatio.get())
	require.Equal(t, map[string][]float64{labels: {3}}, f.hits.get())
	require.Equal(t, map[string][]float64{labels: {1}}, f.misses.get())
	require.Equal(t, map[string][]float64{labels + ",run": {100}}, f.phases.get())

	for _, m := range []*fakeMetric{f.steps, f.missReasons, f.rejected, f.runSecs, f.cpuSecs, f.timeHitRatio} {
		require.Empty(t, m.get())
	}
}

func TestPushBuildMetrics_Steps(t *testing.T) {
	c, f := newBuildMetricsCacheMan()
	pushBuildMetrics(t, c, `{
		"AndroidTarget": "p-userdebug",
		"BuildGoal": "libfoo",
		"Status": "success",
		"BuildDurationSecs": 60,
		"HasNinjaRun": true,
		"CacheHitCount": 3,
		"CacheMissCount": 2,
		"Steps": {
			"RunTypes": {"cached": 2, "delayed-cached": 1, "local": 1, "remote": 1, "some/path": 4},
			"MissReasons": {"mismatch": 1, "no_result": 1, "free text": 2},
			"RejectedResponses": {"skipped": 5, "mismatch": 1, "apply_failed": 1},
			"CacheHitRunSecs": 30,
			"CacheHitCPUSecs": 50,
			"ExecutedRunSecs": 10
		}
	}`)

	labels := "other,success,p-userdebug"
	require.Equal(t, map[string][]float64{
		labels + ",cached":         {2},
		labels + ",delayed-cached": {1},
		labels + ",local":          {1},
		labels + ",remote":         {1},
		labels + ",other":          {4},
	}, f.steps.get())
	require.Equal(t, map[string][]float64{
		labels + ",mismatch":  {1},
		labels + ",no_result": {1},
		labels + ",other":     {2},
	}, f.missReasons.get())
	require.Equal(t, map[string][]float64{
		labels + ",skipped":      {5},
		labels + ",mismatch":     {1},
		labels + ",apply_failed": {1},
	}, f.rejected.get())
	require.Equal(t, map[string][]float64{
		labels + ",cache":    {30},
		labels + ",executed": {10},
	}, f.runSecs.get())
	require.Equal(t, map[string][]float64{labels: {50}}, f.cpuSecs.get())
	require.Equal(t, map[string][]float64{labels: {0.75}}, f.timeHitRatio.get())
}

func TestPushBuildMetrics_StepsWithoutRunTime(t *testing.T) {
	c, f := newBuildMetricsCacheMan()
	pushBuildMetrics(t, c, `{"BuildGoal": "droid", "Status": "success", "HasNinjaRun": true, "Steps": {}}`)

	require.Equal(t, map[string][]float64{
		"droid,success,,cache":    {0},
		"droid,success,,executed": {0},
	}, f.runSecs.get())
	require.Empty(t, f.timeHitRatio.get())
}

func TestPushBuildMetrics_StepsWithoutNinjaRun(t *testing.T) {
	c, f := newBuildMetricsCacheMan()
	pushBuildMetrics(t, c, `{"BuildGoal": "droid", "Status": "failure", "Steps": {"RunTypes": {"local": 1}}}`)

	require.Empty(t, f.steps.get())
	require.Empty(t, f.hits.get())
}

func TestRecordBuildSteps_InvalidSeconds(t *testing.T) {
	for _, v := range []float64{-1, math.Inf(1), math.NaN()} {
		c, f := newBuildMetricsCacheMan()
		c.recordBuildSteps(&protocol.PushBuildMetricsReq{
			Steps: &protocol.BuildStepStats{
				RunTypes:        map[string]uint32{"local": 1},
				CacheHitRunSecs: v,
				ExecutedRunSecs: 1,
			},
		})
		require.Equal(t, map[string][]float64{",,,local": {1}}, f.steps.get())
		require.Empty(t, f.runSecs.get())
		require.Empty(t, f.cpuSecs.get())
		require.Empty(t, f.timeHitRatio.get())
	}
}

func TestPushBuildMetricsReq_Compatibility(t *testing.T) {
	// A newer client's request decodes in a cacheman that does not know Steps.
	type oldReq struct {
		AndroidTarget     string
		BuildGoal         string
		Status            string
		BuildDurationSecs float64
		HasNinjaRun       bool
		CacheHitRatio     float64
		CacheHitCount     uint32
		CacheMissCount    uint32
		Phases            map[string]float64
	}
	b, err := json.Marshal(&protocol.PushBuildMetricsReq{
		BuildGoal:     "droid",
		HasNinjaRun:   true,
		CacheHitCount: 2,
		Steps:         &protocol.BuildStepStats{RunTypes: map[string]uint32{"cached": 2}},
	})
	require.NoError(t, err)
	var old oldReq
	require.NoError(t, json.Unmarshal(b, &old))
	require.Equal(t, "droid", old.BuildGoal)
	require.Equal(t, uint32(2), old.CacheHitCount)

	// An older client's request decodes with nil Steps.
	var req protocol.PushBuildMetricsReq
	require.NoError(t, json.Unmarshal([]byte(oldBuildMetricsReq), &req))
	require.Nil(t, req.Steps)

	// A request without step counts carries no Steps field.
	b, err = json.Marshal(&protocol.PushBuildMetricsReq{BuildGoal: "droid"})
	require.NoError(t, err)
	require.NotContains(t, string(b), "Steps")
}

func TestBoundedLabel(t *testing.T) {
	tests := []struct {
		name  string
		value string
		known []string
		want  string
	}{
		{name: "run type", value: "local-no-tap", known: buildRunTypes, want: "local-no-tap"},
		{name: "unknown run type", value: "fast", known: buildRunTypes, want: "other"},
		{name: "empty run type", value: "", known: buildRunTypes, want: "other"},
		{name: "miss reason", value: "cache_off", known: buildMissReasons, want: "cache_off"},
		{name: "miss reason is case sensitive", value: "Mismatch", known: buildMissReasons, want: "other"},
		{name: "reject reason", value: "apply_failed", known: buildRejectReasons, want: "apply_failed"},
		{name: "no_result is not a reject reason", value: "no_result", known: buildRejectReasons, want: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, boundedLabel(tt.value, tt.known))
		})
	}
}

func TestBuildLabelsMatchProducers(t *testing.T) {
	for _, rt := range []ninjalog.RunType{
		ninjalog.RunTypeLocal,
		ninjalog.RunTypeLocalNoTap,
		ninjalog.RunTypeFallbackBusy,
		ninjalog.RunTypeFallbackErr,
		ninjalog.RunTypeRemote,
		ninjalog.RunTypeCached,
		ninjalog.RunTypeDelayedCached,
	} {
		require.Contains(t, buildRunTypes, string(rt))
	}
	for _, r := range []string{
		binding.CacheReasonOff,
		binding.CacheReasonNoResult,
		binding.CacheReasonSkipped,
		binding.CacheReasonMismatch,
		binding.CacheReasonApplyFailed,
	} {
		require.Contains(t, buildMissReasons, r)
	}
	for _, r := range []string{binding.CacheReasonSkipped, binding.CacheReasonMismatch, binding.CacheReasonApplyFailed} {
		require.Contains(t, buildRejectReasons, r)
	}
}
