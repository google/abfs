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

package builder

import (
	"fmt"
	"os"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/google/abfs/shared/version"
)

const metricPrefix = "buildmanbuilder"

var (
	requestCounter, successCounter, errorCounter                                   prometheus.Counter
	buildInfoTimeMetric, ninjaFirstRunCacheHitMetric, ninjaSecondRunCacheHitMetric prometheus.Observer
	buildStartTimestampMetric                                                      prometheus.Gauge
)

func MetricsForRegistration(gitServer string) ([]prometheus.Collector, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve hostname for buildmanbuilder metrics: %w", err)
	}

	promCounter := func(name, help string) (prometheus.Counter, error) {
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

	requestCounter, err = promCounter("build_request", "Count of build requests received by buildmanbuilder workers")
	if err != nil {
		return nil, err
	}

	successCounter, err = promCounter("success", "Count of buildman runs with build info hash available by buildmanbuilder workers")
	if err != nil {
		return nil, err
	}

	errorCounter, err = promCounter("error", "Count of buildman errors by buildmanbuilder workers")
	if err != nil {
		return nil, err
	}

	buildInfoTimeMetricForRegistration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: metricPrefix + "_build_info_build_duration_seconds",
			Help: "Seconds spent building as recorded by the build info object when it is found",
			Buckets: []float64{
				// seconds
				30, 60,
				// 2-5 minutes by 1
				2 * 60, 3 * 60, 4 * 60, 5 * 60,
				// 10-30 minutes by 5
				10 * 60, 15 * 60, 20 * 60, 25 * 60, 30 * 60,
				// 40-60 minutes by 10
				40 * 60, 50 * 60, 60 * 60,
				// 75-120 minutes by 15
				75 * 60, 90 * 60, 105 * 60, 120 * 60},
		},
		[]string{
			"hostname",
			"version",
			"gitserver",
		},
	)

	buildInfoTimeMetric, err = buildInfoTimeMetricForRegistration.GetMetricWithLabelValues(hostname, version.Version(), gitServer)
	if err != nil {
		return nil, err
	}

	percentBuckets := cacheHitPercentBuckets()

	ninjaFirstRunCacheHitMetricForRegistration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    metricPrefix + "_ninja_first_run_cache_hit_percentage",
			Help:    "Cache hit percentage from the build info's first ninja run info object when it is found",
			Buckets: percentBuckets,
		},
		[]string{
			"hostname",
			"version",
			"gitserver",
		},
	)

	ninjaFirstRunCacheHitMetric, err = ninjaFirstRunCacheHitMetricForRegistration.GetMetricWithLabelValues(hostname, version.Version(), gitServer)
	if err != nil {
		return nil, err
	}

	ninjaSecondRunCacheHitMetricForRegistration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    metricPrefix + "_ninja_second_run_cache_hit_percentage",
			Help:    "Cache hit percentage from the build info's second ninja run info object when it is found",
			Buckets: percentBuckets,
		},
		[]string{
			"hostname",
			"version",
			"gitserver",
		},
	)

	ninjaSecondRunCacheHitMetric, err = ninjaSecondRunCacheHitMetricForRegistration.GetMetricWithLabelValues(hostname, version.Version(), gitServer)
	if err != nil {
		return nil, err
	}

	buildStartTimestampMetric, err = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: metricPrefix + "_build_start_timestamp_seconds",
			Help: "Build start time as recorded in the build start object when it is found",
		},
		[]string{
			"hostname",
			"version",
			"gitserver",
		},
	).GetMetricWithLabelValues(hostname, version.Version(), gitServer)
	if err != nil {
		return nil, err
	}

	return []prometheus.Collector{
			requestCounter,
			successCounter,
			errorCounter,
			buildInfoTimeMetricForRegistration,
			ninjaFirstRunCacheHitMetricForRegistration,
			ninjaSecondRunCacheHitMetricForRegistration,
			buildStartTimestampMetric},
		nil
}

// cacheHitPercentBuckets defines histogram buckets that get increasingly more precise as the
// number approaches 100
func cacheHitPercentBuckets() []float64 {
	percentBuckets := []float64{}

	// go by 1 percents up to `end`
	start := 1
	end := 90

	for i := start; i < end; i++ {
		percentBuckets = append(percentBuckets, float64(i))
	}

	// then by .5 percents up to the new `end`
	start = end
	end = 99
	for i := start; i < end; i++ {
		percentBuckets = append(percentBuckets, float64(i), float64(i)+0.5)
	}

	// then by .05 percents up to 100
	start = end
	end = 100
	for i := start * 100; i <= end*100; i += 5 {
		percentBuckets = append(percentBuckets, float64(i)/100)
	}
	return percentBuckets
}
