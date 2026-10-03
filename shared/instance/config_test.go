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

package instance

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func configStr(c *Config) string {
	b := bytes.NewBuffer(nil)
	if err := c.Encode(b); err != nil {
		panic(err)
	}
	return b.String()
}

func TestConfigToConfig(t *testing.T) {
	require := require.New(t)

	// empty
	require.Equal(&Config{}, (&ConfigOptional{}).ToConfig())

	c1 := &Config{
		CacheDir:      "/etc/cache",
		FSType:        "kmod",
		LocalOnly:     true,
		RemoteServers: []string{"server1", "server2"},
	}

	c2 := c1.NonZeroToOverride().ToConfig()
	require.Equal(c1, c2)
}

func TestConfigSimple(t *testing.T) {
	require := require.New(t)

	require.Equal(``, configStr(&Config{}))

	require.Equal(`cache-dir: /tmp/cache
`, configStr(&Config{
		CacheDir: "/tmp/cache",
	}))

	require.Equal(`cache-dir: /tmp/cache
fs: kmod
`, configStr(&Config{
		CacheDir: "/tmp/cache",
		FSType:   "kmod",
	}))
}

func TestConfigClone(t *testing.T) {
	require := require.New(t)

	require.Equal(``, configStr((&Config{}).Clone()))

	require.Equal(`cache-dir: /tmp/cache
`, configStr((&Config{
		CacheDir: "/tmp/cache",
	}).Clone()))

	require.Equal(`cache-dir: /tmp/cache
fs: kmod
`, configStr((&Config{
		CacheDir: "/tmp/cache",
		FSType:   "kmod",
	}).Clone()))

	c := &Config{
		CacheDir:      "/tmp/cache",
		FSType:        "kmod",
		RemoteServers: []string{"server1", "server2"},
	}

	c2 := c.Clone()
	require.Equal(`remote-servers:
    - server1
    - server2
cache-dir: /tmp/cache
fs: kmod
`, configStr(c2))

	c.RemoteServers[0] = "server3"

	require.Equal(`remote-servers:
    - server1
    - server2
cache-dir: /tmp/cache
fs: kmod
`, configStr(c2))
}

func TestConfigUpdate(t *testing.T) {
	require := require.New(t)

	require.Equal(``, configStr((&Config{}).Clone()))

	c := &Config{
		FSType: "kmod",
	}

	c.Update(&Config{
		CacheDir: "/tmp/cache",
	})
	require.Equal(`cache-dir: /tmp/cache
fs: kmod
`, configStr(c))

	c.Update(&Config{
		CacheDir: "/tmp/cache2",
	})
	require.Equal(`cache-dir: /tmp/cache2
fs: kmod
`, configStr(c))

	c.Update(&Config{
		RemoteServers: []string{"server1", "server2"},
	})
	require.Equal(`remote-servers:
    - server1
    - server2
cache-dir: /tmp/cache2
fs: kmod
`, configStr(c))

	c.Update(&Config{
		RemoteServers: []string{"server3"},
	})
	require.Equal(`remote-servers:
    - server3
cache-dir: /tmp/cache2
fs: kmod
`, configStr(c))
}

func TestConfigWriteRead(t *testing.T) {
	require := require.New(t)
	f, err := os.CreateTemp("", "config-test-*")
	require.NoError(err)
	name := f.Name()
	require.NoError(f.Close())
	defer func() {
		require.NoError(os.Remove(name))
	}()

	c := &Config{
		CacheDir: "/tmp/cache",
		FSType:   "kmod",
	}

	require.NoError(WriteConfig(name, c, false))

	c2, err := ReadConfig[*Config](name)
	require.NoError(err)

	require.Equal(c, c2)
}

func TestConfigWriteAutoGen(t *testing.T) {
	require := require.New(t)
	f, err := os.CreateTemp("", "config-test-*")
	require.NoError(err)
	name := f.Name()
	require.NoError(f.Close())
	defer func() {
		require.NoError(os.Remove(name))
	}()

	c := &Config{
		CacheDir: "/tmp/cache",
		FSType:   "kmod",
	}

	require.NoError(WriteConfig(name, c, true))

	stat, err := os.Stat(name)
	require.NoError(err)
	require.Equal(os.FileMode(0400), stat.Mode())

	c2, err := ReadConfig[*Config](name)
	require.NoError(err)

	require.Equal(c, c2)
}
func TestConfigApplyKeyValues(t *testing.T) {
	require := require.New(t)

	c := &ConfigOptional{}

	// simple string field
	err := c.ApplyKeyValues([]string{"cache-dir=/tmp/foo"})
	require.NoError(err)
	require.Equal("/tmp/foo", *c.CacheDir)

	// simple int/time field
	err = c.ApplyKeyValues([]string{"fetch-timeout=5m"})
	require.NoError(err)
	require.Equal(5*time.Minute, *c.FetchTimeout)

	// slice of strings with multiple entries
	err = c.ApplyKeyValues([]string{"remote-servers=server1", "remote-servers=server2"})
	require.NoError(err)
	require.Equal([]string{"server1", "server2"}, *c.RemoteServers)

	// slice with clear (empty string)
	err = c.ApplyKeyValues([]string{"remote-servers="})
	require.NoError(err)
	require.Empty(*c.RemoteServers)

	// slice of strings with commas in one entry
	err = c.ApplyKeyValues([]string{"remote-servers=server1,server2"})
	require.NoError(err)
	// Comma separated strings are treated as a single element with a comma, given strings get quoted
	require.Equal([]string{"server1,server2"}, *c.RemoteServers)

	// slice of ints (TunnelPorts is []int)
	err = c.ApplyKeyValues([]string{"tunnel-ports=123,456"})
	require.NoError(err)
	// However, integers don't get quoted and this resolves to an array of integers in yaml
	require.Equal([]int{123, 456}, *c.TunnelPorts)

	// slice of ints with multiple entries
	err = c.ApplyKeyValues([]string{"tunnel-ports=123", "tunnel-ports=456"})
	require.NoError(err)
	require.Equal([]int{123, 456}, *c.TunnelPorts)

	// clear a slice of ints
	err = c.ApplyKeyValues([]string{"tunnel-ports="})
	require.NoError(err)
	require.Empty(*c.TunnelPorts)

	// overriding a value (takes last)
	err = c.ApplyKeyValues([]string{"local-only=false", "local-only=true"})
	require.NoError(err)
	require.NotNil(c.LocalOnly)
	require.True(*c.LocalOnly)

	// pointer fields
	err = c.ApplyKeyValues([]string{"enable-index=true"})
	require.NoError(err)
	require.NotNil(c.EnableIndex)
	require.True(*c.EnableIndex)

	// setup.skip-tap-soong-build via kebab-case
	err = c.ApplyKeyValues([]string{"setup.skip-tap-soong-build=true"})
	require.NoError(err)
	require.NotNil(c.SetupSkipTapSoongBuild)
	require.True(*c.SetupSkipTapSoongBuild)

	err = c.ApplyKeyValues([]string{"setup.skip-tap-soong-build=false"})
	require.NoError(err)
	require.NotNil(c.SetupSkipTapSoongBuild)
	require.False(*c.SetupSkipTapSoongBuild)

	// eager-push via kebab-case
	err = c.ApplyKeyValues([]string{"eager-push=true"})
	require.NoError(err)
	require.NotNil(c.EagerPush)
	require.True(*c.EagerPush)

	err = c.ApplyKeyValues([]string{"eager-push=false"})
	require.NoError(err)
	require.NotNil(c.EagerPush)
	require.False(*c.EagerPush)

	// invalid key
	err = c.ApplyKeyValues([]string{"invalid-key=val"})
	require.Error(err)
	require.Contains(err.Error(), "invalid config key")

	// invalid pair (no '=')
	err = c.ApplyKeyValues([]string{"invalid-pair"})
	require.Error(err)
	require.Contains(err.Error(), "invalid key-value pair")
}

func TestConfigApply(t *testing.T) {
	require := require.New(t)

	c := &Config{
		RemoteProject: "project1",
		FSType:        "kmod",
		RemoteServers: []string{"server1"},
	}

	// Override nothing
	c.Apply(&ConfigOptional{})
	require.Equal("project1", c.RemoteProject)
	require.Equal(CASFSType("kmod"), c.FSType)
	require.Equal([]string{"server1"}, c.RemoteServers)

	// Override string and slice
	remoteProject := "project2"
	servers := []string{"server2", "server3"}
	c.Apply(&ConfigOptional{
		RemoteProject: &remoteProject,
		RemoteServers: &servers,
	})
	require.Equal("project2", c.RemoteProject)
	require.Equal(CASFSType("kmod"), c.FSType)
	require.Equal([]string{"server2", "server3"}, c.RemoteServers)

	// Override bool
	localOnly := true
	c.Apply(&ConfigOptional{
		LocalOnly: &localOnly,
	})
	require.True(c.LocalOnly)
}

func TestConfigNonZeroToOverride(t *testing.T) {
	require := require.New(t)

	c := &Config{
		RemoteProject: "project1",
		TunnelPorts:   []int{22000},
		LocalOnly:     true,
	}

	o := c.NonZeroToOverride()

	require.NotNil(o.RemoteProject)
	require.Equal("project1", *o.RemoteProject)

	require.NotNil(o.TunnelPorts)
	require.Equal([]int{22000}, *o.TunnelPorts)

	require.NotNil(o.LocalOnly)
	require.True(*o.LocalOnly)

	// Zero values should be nil in override
	require.Nil(o.RemoteZone)
	require.Nil(o.RemoteServers)

	// Test empty slice is treated as zero
	c2 := &Config{
		RemoteServers: []string{},
	}
	o2 := c2.NonZeroToOverride()
	require.Nil(o2.RemoteServers)
}

func TestConfigOptionalApply(t *testing.T) {
	require := require.New(t)

	c1 := &Config{
		RemoteProject: "project1",
		FSType:        "kmod",
		RemoteServers: []string{"server1"},
	}
	o1 := c1.NonZeroToOverride()

	c2 := &Config{
		RemoteProject: "project1",
		RemoteServers: []string{"server2"},
		FSType:        "kmod",
		LFS:           true,
	}
	o2 := c2.NonZeroToOverride()

	o1.Apply(o2)

	require.Equal(&Config{
		RemoteProject: "project1",
		FSType:        "kmod",
		RemoteServers: []string{"server2"},
		LFS:           true,
	}, o1.ToConfig())
}

func TestConfigOptionalRemoveUnchanged(t *testing.T) {
	require := require.New(t)

	c := &Config{
		RemoteProject: "project1",
		FSType:        "kmod",
		RemoteServers: []string{"server1"},
		LocalOnly:     true,
	}

	o := &ConfigOptional{
		RemoteProject: new(string),
		FSType:        new(CASFSType),
		RemoteServers: &[]string{"server1"},
		LocalOnly:     new(bool),
		TunnelPorts:   &[]int{123},
	}
	*o.RemoteProject = "project1"
	*o.LocalOnly = true

	o.RemoveUnchanged(c)

	// RemoteProject matches, so should be nil
	require.Nil(o.RemoteProject)

	// RemoteServers matches deeply, so should be nil
	require.Nil(o.RemoteServers)

	// LocalOnly matches, so should be nil
	require.Nil(o.LocalOnly)

	// TunnelPorts doesn't match (c has zero value), should be preserved
	require.NotNil(o.TunnelPorts)
	require.Equal([]int{123}, *o.TunnelPorts)
}

func TestConfigReadValidation(t *testing.T) {
	require := require.New(t)

	writeTemp := func(content string) string {
		f, err := os.CreateTemp(t.TempDir(), "config-val-*")
		require.NoError(err)
		_, err = f.WriteString(content)
		require.NoError(err)
		require.NoError(f.Close())
		return f.Name()
	}

	// Unknown field should fail
	_, err := ReadConfig[*ConfigOptional](writeTemp("unknown-field: true\n"))
	require.Error(err)

	// Invalid fs type should fail
	_, err = ReadConfig[*ConfigOptional](writeTemp("fs: invalid_fs\n"))
	require.Error(err)

	// Invalid auth-type should fail
	_, err = ReadConfig[*ConfigOptional](writeTemp("auth-type: invalid_auth\n"))
	require.Error(err)

	// Valid config should pass
	cfg, err := ReadConfig[*ConfigOptional](writeTemp("cache-dir: /tmp/cache\nfs: kmod\n"))
	require.NoError(err)
	require.NotNil(cfg.CacheDir)
	require.Equal("/tmp/cache", *cfg.CacheDir)
	require.NotNil(cfg.FSType)
	require.Equal(CASFSTypeKmod, *cfg.FSType)
}
