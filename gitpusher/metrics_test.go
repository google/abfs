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

package gitpusher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/dagsync"
	"github.com/google/abfs/shared/metrics"
)

// recordingBackend keeps the last value of every metric, keyed by name and
// label values, and every histogram observation.
type recordingBackend struct {
	mu           sync.Mutex
	values       map[string]float64
	observations map[string][]float64
}

func newRecordingBackend() *recordingBackend {
	return &recordingBackend{values: make(map[string]float64), observations: make(map[string][]float64)}
}

func (b *recordingBackend) value(name string, labelValues ...string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.values[name+"{"+strings.Join(labelValues, ",")+"}"]
}

func (b *recordingBackend) isSet(name string, labelValues ...string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.values[name+"{"+strings.Join(labelValues, ",")+"}"]
	return ok
}

func (b *recordingBackend) NewCounter(name, help string, labels []string) metrics.Counter {
	return &recordingMetric{b: b, name: name}
}

func (b *recordingBackend) NewGauge(name, help string, labels []string) metrics.Gauge {
	return &recordingMetric{b: b, name: name}
}

func (b *recordingBackend) NewHistogram(name, help string, buckets []float64, labels []string) metrics.Histogram {
	return &recordingMetric{b: b, name: name}
}

func (b *recordingBackend) Push(ctx context.Context) error  { return nil }
func (b *recordingBackend) Close(ctx context.Context) error { return nil }

type recordingMetric struct {
	b    *recordingBackend
	name string
}

func (m *recordingMetric) key(labelValues []string) string {
	return m.name + "{" + strings.Join(labelValues, ",") + "}"
}

func (m *recordingMetric) Inc(labelValues ...string) { m.Add(1, labelValues...) }

func (m *recordingMetric) Add(val float64, labelValues ...string) {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	m.b.values[m.key(labelValues)] += val
}

func (m *recordingMetric) Set(val float64, labelValues ...string) {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	m.b.values[m.key(labelValues)] = val
}

func (m *recordingMetric) Observe(val float64, labelValues ...string) {
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	m.b.observations[m.key(labelValues)] = append(m.b.observations[m.key(labelValues)], val)
}

func TestPusherMetrics_Sample(t *testing.T) {
	require := require.New(t)
	b := newRecordingBackend()
	m := newPusherMetrics(metrics.WrapBackend(b))
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	stats := []PusherStat{
		{State: "sleep", MirrorBytes: 1000, lastPushed: now.Add(-time.Hour)},
		{State: "sleep", MirrorBytes: 500, lastPushed: now.Add(-time.Minute), LastError: "fetch failed"},
		{State: "push-blobs", MirrorBytes: 4000, DagSync: &dagsync.Stats{States: map[string]map[string]int64{
			"queued": {"blob": 7},
		}}},
		{State: "fetch", MirrorBytes: -1},
	}
	m.sample(stats, 10, now)

	require.Equal(10.0, b.value("abfs_pusher_pool_projects"))
	for st := PushStateCreated; st <= PushStatePushPacks; st++ {
		for _, seeded := range []string{"true", "false"} {
			require.True(b.isSet("abfs_pusher_projects", st.String(), seeded), "state %s seeded %s", st, seeded)
		}
	}
	require.Equal(2.0, b.value("abfs_pusher_projects", "sleep", "true"))
	require.Equal(0.0, b.value("abfs_pusher_projects", "sleep", "false"))
	require.Equal(1.0, b.value("abfs_pusher_projects", "push-blobs", "false"))
	require.Equal(1.0, b.value("abfs_pusher_projects", "fetch", "false"))
	require.Equal(0.0, b.value("abfs_pusher_projects", "diff", "false"))
	require.False(b.isSet("abfs_pusher_projects_total"))
	require.False(b.isSet("abfs_pusher_projects_seeded"))
	require.Equal(1.0, b.value("abfs_pusher_projects_failing"))
	require.Equal(3600.0, b.value("abfs_pusher_oldest_success_age_seconds"))
	require.Equal(1500.0, b.value("abfs_pusher_mirror_bytes", "true"))
	require.Equal(4000.0, b.value("abfs_pusher_mirror_bytes", "false"))
	require.Equal(1.0, b.value("abfs_pusher_projects_without_mirror"))
	require.Equal(7.0, b.value("abfs_pusher_dagsync_requests", "queued", "blob"))

	// A dagsync that finished must not leave its last counts behind.
	m.sample(stats[:2], 10, now)
	require.Equal(0.0, b.value("abfs_pusher_dagsync_requests", "queued", "blob"))
	require.Equal(0.0, b.value("abfs_pusher_projects", "push-blobs", "false"))
}

func TestPusherMetrics_SampleOldestSuccess(t *testing.T) {
	require := require.New(t)
	b := newRecordingBackend()
	m := newPusherMetrics(metrics.WrapBackend(b))
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	// Zero would read as a push that just finished.
	m.sample([]PusherStat{{State: "fetch", MirrorBytes: -1}}, 1, now)
	require.False(b.isSet("abfs_pusher_oldest_success_age_seconds"))

	m.sample([]PusherStat{{State: "sleep", lastPushed: now.Add(-time.Minute)}}, 1, now)
	require.Equal(60.0, b.value("abfs_pusher_oldest_success_age_seconds"))
}

func TestPusherMetrics_SampleRepos(t *testing.T) {
	require := require.New(t)
	b := newRecordingBackend()
	m := newPusherMetrics(metrics.WrapBackend(b))

	m.sampleRepos([]RepoStat{
		{lastSuccess: time.Now()},
		{LastError: "fetch failed"},
		{},
	})
	require.Equal(1.0, b.value("abfs_pusher_repos", "true"))
	require.Equal(2.0, b.value("abfs_pusher_repos", "false"))
	require.False(b.isSet("abfs_pusher_repos_total"))
	require.False(b.isSet("abfs_pusher_repos_without_success"))
	require.Equal(1.0, b.value("abfs_pusher_repos_failing"))

	m.sampleRepos(nil)
	require.Equal(0.0, b.value("abfs_pusher_repos", "true"))
	require.Equal(0.0, b.value("abfs_pusher_repos", "false"))
}

func TestPusherMetrics_RecordPush(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: nil, want: "ok"},
		{err: errors.New("fetch failed"), want: "error"},
		{err: fmt.Errorf("push: %w", context.Canceled), want: "canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			b := newRecordingBackend()
			newPusherMetrics(metrics.WrapBackend(b)).recordPush(time.Second, tt.err)
			require.Equal(t, 1.0, b.value("abfs_pusher_pushes_total", tt.want))
			require.Equal(t, []float64{1}, b.observations["abfs_pusher_push_duration_seconds{"+tt.want+"}"])
		})
	}
}

func TestPackBytes(t *testing.T) {
	require := require.New(t)
	dir := filepath.Join(t.TempDir(), "repo.git")
	require.Equal(int64(-1), packBytes(dir), "a repository that was not fetched has no size")

	packDir := filepath.Join(dir, "objects", "pack")
	require.NoError(os.MkdirAll(packDir, 0o755))
	require.Equal(int64(0), packBytes(dir))

	require.NoError(os.WriteFile(filepath.Join(packDir, "pack-1.pack"), make([]byte, 300), 0o644))
	require.NoError(os.WriteFile(filepath.Join(packDir, "pack-1.idx"), make([]byte, 50), 0o644))
	require.NoError(os.WriteFile(filepath.Join(packDir, "pack-2.pack"), make([]byte, 200), 0o644))
	require.Equal(int64(500), packBytes(dir))
}
