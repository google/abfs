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

// Package telemetry holds the OpenTelemetry resource shared by metrics and traces.
package telemetry

import (
	"context"
	"errors"
	"log"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/google/abfs/shared/metrics"
)

// NewResource describes the process. The environment overrides our defaults,
// and a collector can add attributes of its platform. Call it after
// metrics.SetInstanceName, because the instance name is a default label.
func NewResource(ctx context.Context) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{attribute.String("service.name", "abfs")}
	labels := metrics.DefaultLabels()
	for k, v := range labels {
		switch k {
		case "hostname":
			// Each ABFS instance on a host gets its own series and keeps it
			// across restarts; short command runs of one instance share it.
			id := v
			if name := labels["abfs_instance"]; name != "" {
				id += "/" + name
			}
			attrs = append(attrs, attribute.String("host.name", v), attribute.String("service.instance.id", id))
		case "version":
			attrs = append(attrs, attribute.String("service.version", v))
		default:
			attrs = append(attrs, attribute.String("abfs."+strings.TrimPrefix(k, "abfs_"), v))
		}
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(attrs...),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if errors.Is(err, resource.ErrPartialResource) {
		log.Printf("Warning: telemetry: %v", err)
		return res, nil
	}
	return res, err
}
