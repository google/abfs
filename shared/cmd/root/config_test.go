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

package root

import (
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/instance"
)

func TestConfigFromFlags(t *testing.T) {
	require := require.New(t)

	fl := pflag.NewFlagSet("test", pflag.ContinueOnError)
	var overrides instance.ConfigOptional
	registerConfigFlags(fl, &overrides)

	args := []string{
		"--cache-dir", "/tmp/cache",
		"--local-only",
		"--fetch-timeout", "1m",
		"--remote-servers", "server1",
		"--remote-servers", "server2",
		"--evict-threshold", "10gb",
		"--offline-threshold", "100mb",
		"--tunnel-ports", "1234,5678",
		"--remote-project", "proj-x",
		"--remote-zone", "zone-y",
		"--enable-index",
		"--no-local-cache",
		"--fsync-objects",
		"--auth-audience", "aud-val",
		"--remote-read-only",
		"--git-prefetching",
		"--rbe-as-cas",
		"--no-object-compression",
		"--rbe-instance", "rbe-inst",
		"--rbe-remote", "rbe-rem",
		"--disable-tls",
		"--eager-push",
	}

	err := fl.Parse(args)
	require.NoError(err)

	require.NotNil(overrides.CacheDir)
	require.Equal("/tmp/cache", *overrides.CacheDir)

	require.NotNil(overrides.LocalOnly)
	require.True(*overrides.LocalOnly)

	require.NotNil(overrides.FetchTimeout)
	require.Equal(time.Minute, *overrides.FetchTimeout)

	require.NotNil(overrides.RemoteServers)
	require.Equal([]string{"server1", "server2"}, *overrides.RemoteServers)

	require.NotNil(overrides.EvictThreshold)
	require.Equal(uint64(10*1000*1000*1000), *overrides.EvictThreshold)

	require.NotNil(overrides.OfflineThreshold)
	require.Equal(uint64(100*1000*1000), *overrides.OfflineThreshold)

	require.NotNil(overrides.TunnelPorts)
	require.Equal([]int{1234, 5678}, *overrides.TunnelPorts)

	require.NotNil(overrides.RemoteProject)
	require.Equal("proj-x", *overrides.RemoteProject)

	require.NotNil(overrides.RemoteZone)
	require.Equal("zone-y", *overrides.RemoteZone)

	require.NotNil(overrides.EnableIndex)
	require.True(*overrides.EnableIndex)

	require.NotNil(overrides.NoLocalCache)
	require.True(*overrides.NoLocalCache)

	require.NotNil(overrides.FsyncObjects)
	require.True(*overrides.FsyncObjects)

	require.NotNil(overrides.AuthAudience)
	require.Equal("aud-val", *overrides.AuthAudience)

	require.NotNil(overrides.RemoteReadOnly)
	require.True(*overrides.RemoteReadOnly)

	require.NotNil(overrides.GitPrefetching)
	require.True(*overrides.GitPrefetching)

	require.NotNil(overrides.RBEAsCAS)
	require.True(*overrides.RBEAsCAS)

	require.NotNil(overrides.NoObjectCompression)
	require.True(*overrides.NoObjectCompression)

	require.NotNil(overrides.RBEInstance)
	require.Equal("rbe-inst", *overrides.RBEInstance)

	require.NotNil(overrides.RBERemote)
	require.Equal("rbe-rem", *overrides.RBERemote)

	require.NotNil(overrides.DisableTLS)
	require.True(*overrides.DisableTLS)

	require.NotNil(overrides.EagerPush)
	require.True(*overrides.EagerPush)

	// Unset fields
	require.Nil(overrides.AuthType)
}
