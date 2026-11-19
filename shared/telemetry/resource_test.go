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
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"github.com/google/abfs/shared/metrics"
)

func TestNewResource(t *testing.T) {
	require := require.New(t)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "cloud.region=region-1,service.instance.id=pod-1")
	metrics.SetInstanceName("inst-1")
	t.Cleanup(func() { metrics.SetInstanceName("") })

	res, err := NewResource(context.Background())
	require.NoError(err, "error creating resource")

	got := map[attribute.Key]string{}
	for _, kv := range res.Attributes() {
		got[kv.Key] = kv.Value.String()
	}
	require.Equal("abfs", got["service.name"])
	require.NotEmpty(got["service.version"])
	require.NotEmpty(got["abfs.runID"])
	require.Equal("pod-1", got["service.instance.id"], "the environment must override our defaults")
	require.Equal("region-1", got["cloud.region"])
	require.Equal("inst-1", got["abfs.instance"])
	require.Equal("opentelemetry", got["telemetry.sdk.name"])
	require.Equal("go", got["telemetry.sdk.language"])
	require.NotEmpty(got["telemetry.sdk.version"])
}

func TestNewResource_InstanceID(t *testing.T) {
	host, err := os.Hostname()
	require.NoError(t, err, "error reading host name")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")

	tests := []struct {
		name     string
		instance string
		want     string
	}{
		{name: "instance", instance: "ci-2", want: host + "/ci-2"},
		{name: "default instance", instance: "default", want: host + "/default"},
		{name: "no instance", instance: "", want: host},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics.SetInstanceName(tt.instance)
			t.Cleanup(func() { metrics.SetInstanceName("") })

			res, err := NewResource(context.Background())
			require.NoError(t, err, "error creating resource")

			got := map[attribute.Key]string{}
			for _, kv := range res.Attributes() {
				got[kv.Key] = kv.Value.String()
			}
			require.Equal(t, tt.want, got["service.instance.id"])
			require.Equal(t, host, got["host.name"])
		})
	}
}
