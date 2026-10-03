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
	"os"
	"path/filepath"
	"time"
)

const DefaultFetchTimeout = 2 * time.Minute

var defaultConfig = &Config{
	Class:               "default",
	ManifestScheme:      "https",
	FSType:              "kmod",
	AuthType:            AuthTypeNone,
	FetchTimeout:        DefaultFetchTimeout,
	ManifestProjectName: "platform/manifest",
	EnableIndex:         true,
	EvictThreshold:      uint64(1024 * 1024 * 1024 * 1024), // 1 TB
	FsyncObjects:        true,
	GitPrefetching:      true,
	RefUpdateTimeout:    5 * time.Minute,
	ConcurrentUploads:   20,

	SyncAutoSetup: false,
	SyncPrefetchDirs: []string{
		".repo/repo",
		"build/release",
		"vendor/*/release",
		"vendor/*/build",
		"prebuilts/go",
		"external/golang-protobuf",
		"build/soong",
		"build/blueprint",
		"external/starlark-go",
		"external/pogreb",
	},

	GitPrefetchIndices:  true,
	GitPrefetchSymlinks: true,

	SetupOrderOnlyAsRegular: true,
	SetupRemoteCC:           true,
	SetupRemoteJava:         true,
	SetupRemoteKotlin:       true,
	SetupRemoteMetalava:     true,
	SetupPrefetchInputs:     true,
	SetupPSIMem:             10,
}

func DefaultConfig(instanceDir string) *Config {
	defConfig := defaultConfig.Clone()

	defConfig.CacheDir = filepath.Join(instanceDir, "cache")
	defConfig.LogDir = filepath.Join(instanceDir, "logs")

	rootCAFile := filepath.Join(instanceDir, "abfs_root_ca.crt")
	if _, err := os.Stat(rootCAFile); err == nil {
		defConfig.RootCAFile = rootCAFile
	}
	return defConfig
}
