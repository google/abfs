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

module github.com/google/abfs

// we need to specify the MAJOR.MINOR.PATCH version for go so that the toolset is found during build process if needed
go 1.26.6

replace github.com/iafan/cwalk => ./third_party/cwalk

require (
	al.essio.dev/pkg/shellescape v1.6.0
	cloud.google.com/go/auth v0.23.2
	cloud.google.com/go/longrunning v1.2.0
	cloud.google.com/go/pubsub/v2 v2.7.0
	github.com/DataDog/zstd v1.5.7
	github.com/bazelbuild/remote-apis v0.0.0-20260216160025-715b73f3f9e4
	github.com/bazelbuild/remote-apis-sdks v0.0.0-20260610142741-7ffd493e6686
	github.com/dgraph-io/badger/v4 v4.9.6
	github.com/dustin/go-humanize v1.0.1
	github.com/edsrzf/mmap-go v1.2.0
	github.com/fsnotify/fsnotify v1.10.1
	github.com/gammazero/deque v1.2.1
	github.com/go-git/go-billy/v5 v5.9.1
	github.com/go-git/go-git v4.7.0+incompatible
	github.com/go-git/go-git/v5 v5.19.2
	github.com/gofrs/flock v0.13.1
	github.com/google/ninja-go v0.0.0-20261003052727-73f6cc987759
	github.com/google/uuid v1.6.0
	github.com/gorilla/mux v1.8.1
	github.com/hashicorp/go-version v1.9.0
	github.com/hashicorp/golang-lru/v2 v2.0.7
	github.com/iafan/cwalk v0.0.0-20210125030640-586a8832a711
	github.com/jellydator/ttlcache/v3 v3.4.1
	github.com/mergestat/timediff v0.0.4
	github.com/mwitkow/grpc-proxy v0.0.0-20250813121105-2866842de9a5
	github.com/otiai10/copy v1.14.1
	github.com/pkg/xattr v0.4.12
	github.com/prometheus/client_golang v1.24.1
	github.com/prometheus/procfs v0.22.0
	github.com/samber/go-psi v1.0.1
	github.com/schollz/progressbar/v3 v3.19.1
	github.com/spf13/pflag v1.0.10
	github.com/ssgelm/cookiejarparser v1.1.0
	github.com/stretchr/testify v1.12.1
	go.coder.com/cli v0.6.0
	go.uber.org/multierr v1.11.0
	golang.org/x/exp v0.0.0-20260410095643-746e56fc9e2f
	golang.org/x/mod v0.40.0
	golang.org/x/oauth2 v0.36.0
	golang.org/x/sync v0.22.0
	golang.org/x/sys v0.47.0
	golang.org/x/term v0.45.0
	golang.org/x/text v0.41.0
	google.golang.org/api v0.296.0
	google.golang.org/genproto/googleapis/bytestream v0.0.0-20260831171406-18b4a7587f8a
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260831171406-18b4a7587f8a
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/iam v1.13.0 // indirect
	dario.cat/mergo v1.0.2 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/aviddiviner/go-murmur v0.0.0-20150519214947-b9740d71e571 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudflare/circl v1.6.5 // indirect
	github.com/cyphar/filepath-securejoin v0.7.0 // indirect
	github.com/dgraph-io/ristretto/v2 v2.4.2 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/envoyproxy/go-control-plane/envoy v1.39.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-git/gcfg v1.5.1-0.20230307220236-3a3c6141e376 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang/glog v1.2.5 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.21 // indirect
	github.com/googleapis/gax-go/v2 v2.24.0 // indirect
	github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
	github.com/kevinburke/ssh_config v1.6.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/mitchellh/colorstring v0.0.0-20190213212951-d06e56a500db // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/otiai10/mint v1.6.3 // indirect
	github.com/pjbgf/sha1cd v0.6.0 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.71.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/skeema/knownhosts v1.3.3 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/xanzy/ssh-agent v0.3.3 // indirect
	go.opencensus.io v0.24.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.71.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.71.0 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto v0.0.0-20260831171406-18b4a7587f8a // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260831171406-18b4a7587f8a // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
)
