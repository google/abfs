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

package servers

import (
	"fmt"
	"os"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/google/abfs/shared/version"
)

const metricPrefix = "ares_worker"

var (
	requestCounter, successCounter, errorCounter                                      prometheus.Counter
	fetchDur, preDur, runDur, tapRunDur, tapSnapshotDur, postDur, uploadDur, totalDur prometheus.Observer
)

func MetricsForRegistration(gitServer string) ([]prometheus.Collector, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve hostname for buildmanbuilder metrics: %w", err)
	}

	requestCounter, err = promCounter("request", "Count of requests received by workers", hostname, gitServer)
	if err != nil {
		return nil, err
	}

	successCounter, err = promCounter("success", "Count of responses without errors by workers", hostname, gitServer)
	if err != nil {
		return nil, err
	}

	errorCounter, err = promCounter("error", "Count of errors by workers", hostname, gitServer)
	if err != nil {
		return nil, err
	}

	var fetchForRegistration, preForRegistration, runForRegistration, tapRunForRegistration, tapSnapshotForRegistration, postForRegistration, uploadForRegistration, totalForRegistration *prometheus.HistogramVec
	fetchForRegistration, fetchDur, err = durMetric("eval_fetch", "Duration of eval's fetch step in seconds", hostname, gitServer)
	if err != nil {
		return nil, err
	}
	preForRegistration, preDur, err = durMetric("eval_prepare", "Duration of eval's prepare step in seconds", hostname, gitServer)
	if err != nil {
		return nil, err
	}
	runForRegistration, runDur, err = durMetric("eval_run", "Duration of eval's run step in seconds", hostname, gitServer)
	if err != nil {
		return nil, err
	}
	tapRunForRegistration, tapRunDur, err = durMetric("tap_run", "Duration of tap's run step in seconds", hostname, gitServer)
	if err != nil {
		return nil, err
	}
	tapSnapshotForRegistration, tapSnapshotDur, err = durMetric("tap_snapshot", "Duration of tap's snapshot step in seconds", hostname, gitServer)
	if err != nil {
		return nil, err
	}
	postForRegistration, postDur, err = durMetric("eval_post_process", "Duration of eval's post-process step in seconds", hostname, gitServer)
	if err != nil {
		return nil, err
	}
	uploadForRegistration, uploadDur, err = durMetric("eval_upload", "Duration of eval's upload to remote server in seconds", hostname, gitServer)
	if err != nil {
		return nil, err
	}
	totalForRegistration, totalDur, err = durMetric("eval_total", "Duration of eval seconds", hostname, gitServer)
	if err != nil {
		return nil, err
	}

	return []prometheus.Collector{
			requestCounter,
			successCounter,
			errorCounter,
			fetchForRegistration,
			preForRegistration,
			runForRegistration,
			tapRunForRegistration,
			tapSnapshotForRegistration,
			postForRegistration,
			uploadForRegistration,
			totalForRegistration,
		},
		nil
}

func promCounter(name, help, hostname, gitServer string) (prometheus.Counter, error) {
	return prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: fmt.Sprintf("%s_%s_total", metricPrefix, name),
			Help: help,
		},
		[]string{
			"hostname",
			"version",
			"gitserver",
		},
	).GetMetricWithLabelValues(hostname, version.Version(), gitServer)
}

func durMetric(name, help, hostname, gitServer string) (*prometheus.HistogramVec, prometheus.Observer, error) {
	forRegistration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    fmt.Sprintf("%s_%s_seconds", metricPrefix, name),
			Help:    help,
			Buckets: secondBuckets(),
		},
		[]string{
			"hostname",
			"version",
			"gitserver",
		},
	)
	obs, err := forRegistration.GetMetricWithLabelValues(hostname, version.Version(), gitServer)
	return forRegistration, obs, err
}

func secondBuckets() []float64 {
	var buckets = []float64{
		// --- Millisecond resolution ---
		0.01,  // 10ms
		0.025, // 25ms
		0.05,  // 50ms
		0.1,   // 100ms
		0.25,  // 250ms
		0.5,   // 500ms

		// --- Second resolution ---
		1,   // 1s
		2.5, // 2.5s
		5,   // 5s
		10,  // 10s
		30,  // 30s

		// --- Minute resolution ---
		60,   // 1 min
		300,  // 5 min
		600,  // 10 min
		900,  // 15 min
		1800, // 30 min
		3600, // 60 min

		// --- Hour resolution
		2 * 60 * 60, // 2 hours
		4 * 60 * 60, // 4 hours
	}

	return buckets
}
