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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/abfs/shared/metrics"
)

const metricsInterval = 30 * time.Second

var pushDurationBuckets = []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600, 7200, 14400}

// pusherMetrics records what operators ask of a pusher: how far the seed is,
// which projects fail or fall behind, and how fast data moves.
type pusherMetrics struct {
	pushes        metrics.Counter
	pushDuration  metrics.Histogram
	projects      metrics.Gauge
	poolProjects  metrics.Gauge
	failing       metrics.Gauge
	oldestSuccess metrics.Gauge
	mirrorBytes   metrics.Gauge
	noMirror      metrics.Gauge
	dagRequests   metrics.Gauge
	transferred   metrics.Counter
	repos         metrics.Gauge
	reposFailing  metrics.Gauge

	// dagKeys are the dagsync state and type pairs reported last time, so
	// pairs that disappear are reported as zero instead of keeping old values.
	dagKeys map[[2]string]bool
}

func newPusherMetrics(provider metrics.Provider) *pusherMetrics {
	return &pusherMetrics{
		pushes:        provider.NewCounter("abfs_pusher_pushes_total", "Count of project pushes by result.", []string{"result"}),
		pushDuration:  provider.NewHistogram("abfs_pusher_push_duration_seconds", "Duration of a project push in seconds, from fetch to the end of upload.", pushDurationBuckets, []string{"result"}),
		projects:      provider.NewGauge("abfs_pusher_projects", "Count of projects assigned to this pusher by push state and whether they were pushed successfully at least once.", []string{"state", "seeded"}),
		poolProjects:  provider.NewGauge("abfs_pusher_pool_projects", "Count of projects in the manifests of the whole pusher pool.", nil),
		failing:       provider.NewGauge("abfs_pusher_projects_failing", "Count of assigned projects whose last push failed.", nil),
		oldestSuccess: provider.NewGauge("abfs_pusher_oldest_success_age_seconds", "Seconds since the least recent last successful push among seeded projects.", nil),
		mirrorBytes:   provider.NewGauge("abfs_pusher_mirror_bytes", "Size of the git packs in the local mirrors, split by whether the project is seeded.", []string{"seeded"}),
		noMirror:      provider.NewGauge("abfs_pusher_projects_without_mirror", "Count of assigned projects not fetched yet, whose size is unknown.", nil),
		dagRequests:   provider.NewGauge("abfs_pusher_dagsync_requests", "Count of dagsync upload requests by state and type, over running pushes.", []string{"state", "type"}),
		transferred:   provider.NewCounter("abfs_pusher_transferred_bytes_total", "Bytes sent to and received from the ABFS server.", []string{"direction"}),
		repos:         provider.NewGauge("abfs_pusher_repos", "Count of watched manifest repositories by whether they were listed successfully at least once.", []string{"listed"}),
		reposFailing:  provider.NewGauge("abfs_pusher_repos_failing", "Count of watched manifest repositories whose last listing failed.", nil),
		dagKeys:       make(map[[2]string]bool),
	}
}

func pushResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "error"
	}
}

func (m *pusherMetrics) recordPush(d time.Duration, err error) {
	result := pushResult(err)
	m.pushes.Inc(result)
	m.pushDuration.Observe(d.Seconds(), result)
}

func (m *pusherMetrics) sample(stats []PusherStat, poolProjects int, now time.Time) {
	states := make(map[[2]string]int)
	for st := PushStateCreated; st <= PushStatePushPacks; st++ {
		states[[2]string{st.String(), "true"}] = 0
		states[[2]string{st.String(), "false"}] = 0
	}
	dag := make(map[[2]string]int64)
	var failing, noMirror int
	var mirrorSeeded, mirrorPending int64
	var oldest time.Time
	for _, st := range stats {
		isSeeded := !st.lastPushed.IsZero()
		states[[2]string{st.State, strconv.FormatBool(isSeeded)}]++
		if st.LastError != "" {
			failing++
		}
		if isSeeded {
			if oldest.IsZero() || st.lastPushed.Before(oldest) {
				oldest = st.lastPushed
			}
		}
		switch {
		case st.MirrorBytes < 0:
			noMirror++
		case isSeeded:
			mirrorSeeded += st.MirrorBytes
		default:
			mirrorPending += st.MirrorBytes
		}
		if st.DagSync != nil {
			for state, types := range st.DagSync.States {
				for typ, count := range types {
					dag[[2]string{state, typ}] += count
				}
			}
		}
	}

	for key, count := range states {
		m.projects.Set(float64(count), key[0], key[1])
	}
	m.poolProjects.Set(float64(poolProjects))
	m.failing.Set(float64(failing))
	// Zero would read as a push that just finished, so leave the age unset
	// until a project is seeded.
	if !oldest.IsZero() {
		m.oldestSuccess.Set(now.Sub(oldest).Seconds())
	}
	m.mirrorBytes.Set(float64(mirrorSeeded), "true")
	m.mirrorBytes.Set(float64(mirrorPending), "false")
	m.noMirror.Set(float64(noMirror))

	for key := range m.dagKeys {
		if _, ok := dag[key]; !ok {
			m.dagRequests.Set(0, key[0], key[1])
		}
	}
	m.dagKeys = make(map[[2]string]bool, len(dag))
	for key, count := range dag {
		m.dagRequests.Set(float64(count), key[0], key[1])
		m.dagKeys[key] = true
	}
}

func (m *pusherMetrics) sampleRepos(stats []RepoStat) {
	var listed, failing int
	for _, st := range stats {
		if !st.lastSuccess.IsZero() {
			listed++
		}
		if st.LastError != "" {
			failing++
		}
	}
	m.repos.Set(float64(listed), "true")
	m.repos.Set(float64(len(stats)-listed), "false")
	m.reposFailing.Set(float64(failing))
}

// recordMetrics samples the schedulers every metricsInterval until ctx is done.
func (g *GitPusher) recordMetrics(ctx context.Context, sched *pushersScheduler, repoSched *repoScheduler) {
	ticker := time.NewTicker(metricsInterval)
	defer ticker.Stop()
	var sent, received int64
	for {
		stats, poolProjects := sched.projectsStats()
		g.metrics.sample(stats, poolProjects, time.Now())
		g.metrics.sampleRepos(repoSched.ReposStats())
		stat := g.abfsClient.Stat()
		g.metrics.transferred.Add(float64(stat.BytesSent-sent), "sent")
		g.metrics.transferred.Add(float64(stat.BytesReceived-received), "received")
		sent, received = stat.BytesSent, stat.BytesReceived

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// packBytes returns the size of the pack files of a bare git repository, or -1
// when the repository has not been fetched yet.
func packBytes(gitDir string) int64 {
	entries, err := os.ReadDir(filepath.Join(gitDir, "objects", "pack"))
	if err != nil {
		return -1
	}
	var total int64
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".pack") {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}
