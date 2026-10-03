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

import "time"

type Config struct {
	Class               string        `yaml:"class,omitempty" json:",omitempty" desc:"Client class name"`
	RemoteServers       []string      `yaml:"remote-servers,omitempty" json:",omitempty" desc:"Remote ABFS server addresses"`
	RemoteProject       string        `yaml:"remote-project,omitempty" json:",omitempty" desc:"GCP project of the buildman Pub/Sub topics and subscriptions"`
	RemoteZone          string        `yaml:"remote-zone,omitempty" json:",omitempty" desc:"Deprecated and ignored"`
	TunnelPorts         []int         `yaml:"tunnel-ports,omitempty" json:",omitempty" desc:"Local ports for SSH port-forwarding"`
	LocalOnly           bool          `yaml:"local-only,omitempty" json:",omitempty" desc:"Run locally without remote server access"`
	ManifestScheme      string        `yaml:"manifest-scheme,omitempty" json:",omitempty" desc:"Manifest URL scheme"`
	ManifestServer      string        `yaml:"manifest-server,omitempty" json:",omitempty" desc:"Manifest server hostname"`
	BuildDashboardURL   string        `yaml:"build-dashboard-url,omitempty" json:",omitempty" desc:"Build dashboard URL"`
	CacheDir            string        `yaml:"cache-dir,omitempty" json:",omitempty" desc:"Local ABFS cache directory"`
	LogDir              string        `yaml:"log-dir,omitempty" json:",omitempty" desc:"Directory for ABFS logs"`
	FetchTimeout        time.Duration `yaml:"fetch-timeout,omitempty" json:",omitempty" desc:"Timeout for fetching remote objects (e.g. 5m)"`
	FSType              CASFSType     `yaml:"fs,omitempty" json:",omitempty" desc:"Filesystem type"`
	ManifestProjectName string        `yaml:"manifest-project-name,omitempty" json:",omitempty" desc:"Manifest project name on server"`
	RecordClientMetrics bool          `yaml:"record-client-metrics,omitempty" json:",omitempty" desc:"Record client telemetry metrics (requires a metrics provider)"`
	MetricsProvider     string        `yaml:"metrics-provider,omitempty" json:",omitempty" desc:"Telemetry metrics provider registered with metrics.Register (none are built in)"`
	LFS                 bool          `yaml:"lfs,omitempty" json:",omitempty" desc:"Enable Git LFS support"`
	NoLocalCache        bool          `yaml:"no-local-cache,omitempty" json:",omitempty" desc:"Bypass local cache and connect directly to server"`
	AuthType            AuthType      `yaml:"auth-type,omitempty" json:",omitempty" desc:"Authentication type"`
	AuthAudience        string        `yaml:"auth-audience,omitempty" json:",omitempty" desc:"Audience for authentication token"`
	RootCAFile          string        `yaml:"root-ca-file,omitempty" json:",omitempty" desc:"Custom root CA certificate file path"`
	DisableTLS          bool          `yaml:"disable-tls,omitempty" json:",omitempty" desc:"Disable TLS for remote connections"`
	RemoteReadOnly      bool          `yaml:"remote-read-only,omitempty" json:",omitempty" desc:"Run with read-only access to remote servers"`
	PartialClone        bool          `yaml:"partial-clone,omitempty" json:",omitempty" desc:"Enable Git partial clone"`
	EnableIndex         bool          `yaml:"enable-index,omitempty" json:",omitempty" desc:"Enable localcache object index"`
	EvictThreshold      uint64        `yaml:"evict-threshold,omitempty" json:",omitempty" desc:"Localcache eviction size threshold in bytes"`
	OfflineThreshold    uint64        `yaml:"offline-threshold,omitempty" json:",omitempty" desc:"Size threshold in bytes for offline-only storage"`
	FsyncObjects        bool          `yaml:"fsync-objects,omitempty" json:",omitempty" desc:"Fsync objects when writing to localcache"`
	GitPrefetching      bool          `yaml:"git-prefetching,omitempty" json:",omitempty" desc:"Prefetch Git objects to accelerate operations"`
	ConcurrentUploads   int           `yaml:"concurrent-uploads,omitempty" json:",omitempty" desc:"Number of concurrent object uploads"`
	RefUpdateTimeout    time.Duration `yaml:"ref-update-timeout,omitempty" json:",omitempty" desc:"Timeout for updating refs"`
	RepoDefault         bool          `yaml:"repo-default,omitempty" json:",omitempty" desc:"Use ABFS as default for repo checkouts"`
	NoObjectCompression bool          `yaml:"no-object-compression,omitempty" json:",omitempty" desc:"Disable zstd object compression"`
	RBEAsCAS            bool          `yaml:"rbe-as-cas,omitempty" json:",omitempty" desc:"Use RBE as primary remote CAS client"`
	RBEInstance         string        `yaml:"rbe-instance,omitempty" json:",omitempty" desc:"RBE instance name (required if rbe-as-cas is true)"`
	RBERemote           string        `yaml:"rbe-remote,omitempty" json:",omitempty" desc:"RBE remote address (required if rbe-as-cas is true)"`
	AutoSetup           bool          `yaml:"auto-setup,omitempty" json:",omitempty" desc:"Automatically run setup when mounting"`
	WriteToConfig       bool          `yaml:"write-to-config,omitempty" json:",omitempty" desc:"Persist command-line flags to config file"`
	ForceWorkspaces     bool          `yaml:"force-workspaces,omitempty" json:",omitempty" desc:"Force workspace mode"`
	IgnoreSoongEnvAvail bool          `yaml:"ignore-soong-env-avail,omitempty" json:",omitempty" desc:"Ignore Soong environment availability check"`
	EagerPush           bool          `yaml:"eager-push,omitempty" json:",omitempty" desc:"Eagerly push local objects to remote server"`

	SyncAutoSetup    bool     `yaml:"sync.auto-setup,omitempty" json:",omitempty" desc:"Automatically run setup after sync"`
	SyncPrefetchDirs []string `yaml:"sync.prefetch-dirs,omitempty" json:",omitempty" desc:"Directories to prefetch during sync"`

	GitPrefetchIndices  bool `yaml:"git.prefetch-indices,omitempty" json:",omitempty" desc:"Prefetch Git index files"`
	GitPrefetchSymlinks bool `yaml:"git.prefetch-symlinks,omitempty" json:",omitempty" desc:"Prefetch Git symlinks"`

	SetupIgnoreEvalCacheRegex string  `yaml:"setup.ignore-eval-cache-regex,omitempty" json:",omitempty" desc:"Regex of targets to exclude from eval cache"`
	SetupLogMismatches        bool    `yaml:"setup.log-mismatches,omitempty" json:",omitempty" desc:"Log eval cache mismatches"`
	SetupPSICPU               float64 `yaml:"setup.psi-cpu,omitempty" json:",omitempty" desc:"PSI CPU pressure stall threshold"`
	SetupPSIIO                float64 `yaml:"setup.psi-io,omitempty" json:",omitempty" desc:"PSI I/O pressure stall threshold"`
	SetupPSIMem               float64 `yaml:"setup.psi-mem,omitempty" json:",omitempty" desc:"PSI memory pressure stall threshold"`
	SetupOrderOnlyAsRegular   bool    `yaml:"setup.order-only-as-regular,omitempty" json:",omitempty" desc:"Treat order-only dependencies as regular dependencies"`
	SetupNoLocalFallback      bool    `yaml:"setup.no-local-fallback,omitempty" json:",omitempty" desc:"Disable local fallback on remote build failure"`
	SetupEvalSalt             string  `yaml:"setup.eval-salt,omitempty" json:",omitempty" desc:"Salt string added to eval cache keys"`
	SetupEvalNoCache          bool    `yaml:"setup.eval-no-cache,omitempty" json:",omitempty" desc:"Disable eval cache lookup"`
	SetupNoARES               bool    `yaml:"setup.no-ares,omitempty" json:",omitempty" desc:"Disable ARES integration during setup"`
	SetupRemoteBootstrap      bool    `yaml:"setup.remote-bootstrap,omitempty" json:",omitempty" desc:"Enable remote bootstrap execution"`
	SetupRemoteCC             bool    `yaml:"setup.remote-cc,omitempty" json:",omitempty" desc:"Enable remote C/C++ compilation"`
	SetupRemoteJava           bool    `yaml:"setup.remote-java,omitempty" json:",omitempty" desc:"Enable remote Java compilation"`
	SetupRemoteRust           bool    `yaml:"setup.remote-rust,omitempty" json:",omitempty" desc:"Enable remote Rust compilation"`
	SetupRemoteKotlin         bool    `yaml:"setup.remote-kotlin,omitempty" json:",omitempty" desc:"Enable remote Kotlin compilation"`
	SetupRemoteMetalava       bool    `yaml:"setup.remote-metalava,omitempty" json:",omitempty" desc:"Enable remote Metalava execution"`
	SetupStrictTapping        bool    `yaml:"setup.strict-tapping,omitempty" json:",omitempty" desc:"Fail build if tapping encounters unsupported construct"`
	SetupPrefetchInputs       bool    `yaml:"setup.prefetch-inputs,omitempty" json:",omitempty" desc:"Prefetch action inputs before execution"`
	SetupSkipTapSoongBuild    bool    `yaml:"setup.skip-tap-soong-build,omitempty" json:",omitempty" desc:"Skip tapping Soong build actions"`
}

type ConfigOptional struct {
	Class               *string        `yaml:"class,omitempty" json:",omitempty"`
	RemoteServers       *[]string      `yaml:"remote-servers,omitempty" json:",omitempty"`
	RemoteProject       *string        `yaml:"remote-project,omitempty" json:",omitempty"`
	RemoteZone          *string        `yaml:"remote-zone,omitempty" json:",omitempty"`
	TunnelPorts         *[]int         `yaml:"tunnel-ports,omitempty" json:",omitempty"`
	LocalOnly           *bool          `yaml:"local-only,omitempty" json:",omitempty"`
	ManifestScheme      *string        `yaml:"manifest-scheme,omitempty" json:",omitempty"`
	ManifestServer      *string        `yaml:"manifest-server,omitempty" json:",omitempty"`
	BuildDashboardURL   *string        `yaml:"build-dashboard-url,omitempty" json:",omitempty"`
	CacheDir            *string        `yaml:"cache-dir,omitempty" json:",omitempty"`
	LogDir              *string        `yaml:"log-dir,omitempty" json:",omitempty"`
	FetchTimeout        *time.Duration `yaml:"fetch-timeout,omitempty" json:",omitempty"`
	FSType              *CASFSType     `yaml:"fs,omitempty" json:",omitempty"`
	ManifestProjectName *string        `yaml:"manifest-project-name,omitempty" json:",omitempty"`
	RecordClientMetrics *bool          `yaml:"record-client-metrics,omitempty" json:",omitempty"`
	MetricsProvider     *string        `yaml:"metrics-provider,omitempty" json:",omitempty"`
	LFS                 *bool          `yaml:"lfs,omitempty" json:",omitempty"`
	NoLocalCache        *bool          `yaml:"no-local-cache,omitempty" json:",omitempty"`
	AuthType            *AuthType      `yaml:"auth-type,omitempty" json:",omitempty"`
	AuthAudience        *string        `yaml:"auth-audience,omitempty" json:",omitempty"`
	RootCAFile          *string        `yaml:"root-ca-file,omitempty" json:",omitempty"`
	DisableTLS          *bool          `yaml:"disable-tls,omitempty" json:",omitempty"`
	RemoteReadOnly      *bool          `yaml:"remote-read-only,omitempty" json:",omitempty"`
	PartialClone        *bool          `yaml:"partial-clone,omitempty" json:",omitempty"`
	EnableIndex         *bool          `yaml:"enable-index,omitempty" json:",omitempty"`
	EvictThreshold      *uint64        `yaml:"evict-threshold,omitempty" json:",omitempty"`
	OfflineThreshold    *uint64        `yaml:"offline-threshold,omitempty" json:",omitempty"`
	FsyncObjects        *bool          `yaml:"fsync-objects,omitempty" json:",omitempty"`
	GitPrefetching      *bool          `yaml:"git-prefetching,omitempty" json:",omitempty"`
	ConcurrentUploads   *int           `yaml:"concurrent-uploads,omitempty" json:",omitempty"`
	RefUpdateTimeout    *time.Duration `yaml:"ref-update-timeout,omitempty" json:",omitempty"`
	RepoDefault         *bool          `yaml:"repo-default,omitempty" json:",omitempty"`
	NoObjectCompression *bool          `yaml:"no-object-compression,omitempty" json:",omitempty"`
	RBEAsCAS            *bool          `yaml:"rbe-as-cas,omitempty" json:",omitempty"`
	RBEInstance         *string        `yaml:"rbe-instance,omitempty" json:",omitempty"`
	RBERemote           *string        `yaml:"rbe-remote,omitempty" json:",omitempty"`
	AutoSetup           *bool          `yaml:"auto-setup,omitempty" json:",omitempty"`
	WriteToConfig       *bool          `yaml:"write-to-config,omitempty" json:",omitempty"`
	ForceWorkspaces     *bool          `yaml:"force-workspaces,omitempty" json:",omitempty"`
	IgnoreSoongEnvAvail *bool          `yaml:"ignore-soong-env-avail,omitempty" json:",omitempty"`
	EagerPush           *bool          `yaml:"eager-push,omitempty" json:",omitempty"`

	SyncAutoSetup    *bool     `yaml:"sync.auto-setup,omitempty" json:",omitempty"`
	SyncPrefetchDirs *[]string `yaml:"sync.prefetch-dirs,omitempty" json:",omitempty"`

	GitPrefetchIndices  *bool `yaml:"git.prefetch-indices,omitempty" json:",omitempty"`
	GitPrefetchSymlinks *bool `yaml:"git.prefetch-symlinks,omitempty" json:",omitempty"`

	SetupIgnoreEvalCacheRegex *string  `yaml:"setup.ignore-eval-cache-regex,omitempty" json:",omitempty"`
	SetupLogMismatches        *bool    `yaml:"setup.log-mismatches,omitempty" json:",omitempty"`
	SetupPSICPU               *float64 `yaml:"setup.psi-cpu,omitempty" json:",omitempty"`
	SetupPSIIO                *float64 `yaml:"setup.psi-io,omitempty" json:",omitempty"`
	SetupPSIMem               *float64 `yaml:"setup.psi-mem,omitempty" json:",omitempty"`
	SetupOrderOnlyAsRegular   *bool    `yaml:"setup.order-only-as-regular,omitempty" json:",omitempty"`
	SetupNoLocalFallback      *bool    `yaml:"setup.no-local-fallback,omitempty" json:",omitempty"`
	SetupEvalSalt             *string  `yaml:"setup.eval-salt,omitempty" json:",omitempty"`
	SetupEvalNoCache          *bool    `yaml:"setup.eval-no-cache,omitempty" json:",omitempty"`
	SetupNoARES               *bool    `yaml:"setup.no-ares,omitempty" json:",omitempty"`
	SetupRemoteBootstrap      *bool    `yaml:"setup.remote-bootstrap,omitempty" json:",omitempty"`
	SetupRemoteCC             *bool    `yaml:"setup.remote-cc,omitempty" json:",omitempty"`
	SetupRemoteJava           *bool    `yaml:"setup.remote-java,omitempty" json:",omitempty"`
	SetupRemoteRust           *bool    `yaml:"setup.remote-rust,omitempty" json:",omitempty"`
	SetupRemoteKotlin         *bool    `yaml:"setup.remote-kotlin,omitempty" json:",omitempty"`
	SetupRemoteMetalava       *bool    `yaml:"setup.remote-metalava,omitempty" json:",omitempty"`
	SetupStrictTapping        *bool    `yaml:"setup.strict-tapping,omitempty" json:",omitempty"`
	SetupPrefetchInputs       *bool    `yaml:"setup.prefetch-inputs,omitempty" json:",omitempty"`
	SetupSkipTapSoongBuild    *bool    `yaml:"setup.skip-tap-soong-build,omitempty" json:",omitempty"`
}
