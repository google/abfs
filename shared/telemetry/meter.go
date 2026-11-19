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

package telemetry

import (
	"sync/atomic"

	"go.opentelemetry.io/otel/metric"
)

var meterProvider atomic.Pointer[metric.MeterProvider]

// SetMeterProvider records the meter provider of the metrics backend, so that
// code outside shared/metrics, such as gRPC stats handlers, records into the
// same pipeline. A nil provider clears it.
func SetMeterProvider(mp metric.MeterProvider) {
	if mp == nil {
		meterProvider.Store(nil)
		return
	}
	meterProvider.Store(&mp)
}

// MeterProvider returns the meter provider of the metrics backend, or nil
// when metrics are off.
func MeterProvider() metric.MeterProvider {
	if mp := meterProvider.Load(); mp != nil {
		return *mp
	}
	return nil
}
