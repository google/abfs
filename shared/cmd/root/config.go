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
	"fmt"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd/optflags"
	"github.com/google/abfs/shared/instance"
)

func registerConfigFlags(fl *pflag.FlagSet, c *instance.ConfigOptional) {
	optflags.OptDuration(fl, &c.FetchTimeout, "fetch-timeout", "", "The timeout for fetching remote objects (override config)")
	optflags.OptString(fl, &c.CacheDir, "cache-dir", "c", fmt.Sprintf("The %s cache directory (override config)", flavor.Name))
	optflags.OptBool(fl, &c.LocalOnly, "local-only", "l", fmt.Sprintf("Run %s locally without remote access (override config)", flavor.Name))
	optflags.OptVar(fl, &c.AuthType, "auth-type", "", fmt.Sprintf("Auth type [%s] (override config)", instance.AuthTypes()))
	optflags.OptString(fl, &c.RootCAFile, "root-ca-file", "", "If provided, this is used as the root CA rather than system defaults")
	optflags.OptStringSlice(fl, &c.RemoteServers, "remote-servers", "s", "The remote servers to talk to (override config)")
	optflags.OptIntSlice(fl, &c.TunnelPorts, "tunnel-ports", "t", "The local ports to use for ssh port-forwarding, zero means no port-forwarding (override config)")
	optflags.OptString(fl, &c.RemoteProject, "remote-project", "", "The GCP project of the buildman Pub/Sub topics and subscriptions")
	optflags.OptString(fl, &c.RemoteZone, "remote-zone", "", "Deprecated and ignored")
	if err := fl.MarkDeprecated("remote-zone", "it is no longer used"); err != nil {
		panic(err)
	}
	optflags.OptBool(fl, &c.EnableIndex, "enable-index", "", "Enable the localcache index to track objects")
	optflags.OptSize(fl, &c.EvictThreshold, "evict-threshold", "", "Size threshold of localcache before eviction (e.g., 10GB, 500MB). A zero-value is considered unbounded.")
	optflags.OptSize(fl, &c.OfflineThreshold, "offline-threshold", "", "Size threshold of objects to force offline-only storage (e.g., 10GB, 500MB). A zero-value is considered unbounded.")
	optflags.OptBool(fl, &c.NoLocalCache, "no-local-cache", "", "Do not use local cache and connect to server directly (override config)")
	optflags.OptBool(fl, &c.FsyncObjects, "fsync-objects", "", "Fsync objects when writing to localcache (override config)")
	optflags.OptString(fl, &c.AuthAudience, "auth-audience", "", "The audience to use for the auth token (override config)")
	optflags.OptBool(fl, &c.RemoteReadOnly, "remote-read-only", "", "Run ABFS with remote read-only access (override config)")
	optflags.OptBool(fl, &c.GitPrefetching, "git-prefetching", "", "Prefetch git objects to speed up git operations")
	optflags.OptVar(fl, &c.FSType, "fs", "", fmt.Sprintf("Filesystem type [%s] (override config)", instance.CASFSTypes()))
	optflags.OptBool(fl, &c.LFS, "lfs", "", "Use Git LFS (override config)")
	optflags.OptString(fl, &c.ManifestScheme, "manifest-scheme", "", "The manifest scheme to assume (override config).")
	optflags.OptString(fl, &c.ManifestServer, "manifest-server", "m", "The manifest server to assume (override config).")
	optflags.OptString(fl, &c.ManifestProjectName, "manifest-project-name", "", "The manifest project name on the manifest server (override config).")
	optflags.OptBool(fl, &c.WriteToConfig, "write-to-config", "", "Overwrite the config file with the flags passed to this command")
	optflags.OptBool(fl, &c.DisableTLS, "disable-tls", "", "Disable TLS for remote connections (override config)")
	optflags.OptBool(fl, &c.EagerPush, "eager-push", "", "Eagerly push local objects to the remote server (override config)")

	// RBE flags
	optflags.OptBool(fl, &c.RBEAsCAS, "rbe-as-cas", "", "Use RBE as the primary remote CAS client (override config)")
	optflags.OptBool(fl, &c.NoObjectCompression, "no-object-compression", "", "Disable object compression (override config)")
	optflags.OptString(fl, &c.RBEInstance, "rbe-instance", "", "RBE instance name (override config)")
	optflags.OptString(fl, &c.RBERemote, "rbe-remote", "", "RBE remote address (override config)")
}
