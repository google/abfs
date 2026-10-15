# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

SHELL := /bin/bash

include makefiles/checks.mk
include makefiles/version.mk
include makefiles/destdir.mk

VERSION_LDFLAGS := -X github.com/google/abfs/shared/version.version=$(VERSION)

# Optional: set EXPIRATION_EPOCH_SEC to build abfs binaries that expire at that
# time (see `abfs expiration`). Empty means no expiration.

.PHONY: all all-non-abfs abfs build-abfs ninjago abfsbox abfs-wrap-no-cgo tap-no-cgo ninjago-no-cgo abfsbox-no-cgo casfs-mounter casfuse-mounter install install-client install-pusher install-server maybe-copy-license doc abfs-bazel-daemon abfs-bazel-daemon-no-cgo install-hooks abfs-resources rbetap rbetap-no-cgo abfs-rbe

all: install-hooks abfs all-non-abfs
	$(info )
	$(info Installed in $(DESTDIR)! Run 'abfs init' if this is your first time using abfs, otherwise run 'abfs cacheman restart'.)

all-non-abfs: cacheman ninjago abfsbox abfs-wrap-no-cgo tap-no-cgo ninjago-no-cgo casfs-mounter casfuse-mounter abfsbox-no-cgo abfs-cmd-map-no-cgo abfs-bazel-daemon abfs-bazel-daemon-no-cgo abfs-resources rbetap-no-cgo abfs-rbe

install-hooks:
	@if [[ ! -f .git/hooks/pre-commit ]] || ! diff -q .git/hooks/pre-commit hooks/pre-commit; then \
		echo "Updating pre-commit hooks..."; \
		mkdir -p .git/hooks; \
		cp hooks/pre-commit .git/hooks/.; \
		chmod +x .git/hooks/pre-commit; \
	fi

maybe-copy-license:
	@if command -v "cp" > /dev/null; then \
		cp LICENSE cmd/abfs/license/LICENSE; \
	fi

cacheman: shared/cacheman/cmd/cacheman/cacheman.go
	go install -ldflags="$(VERSION_LDFLAGS)" shared/cacheman/cmd/cacheman/cacheman.go

MUSL_CC ?= $(shell which musl-gcc 2>/dev/null || echo musl-gcc)

cacheman-static:
	@if ! command -v $(MUSL_CC) >/dev/null 2>&1; then 		echo "ERROR: $(MUSL_CC) not found. Please install musl-tools ('sudo apt install musl-tools') or specify MUSL_CC=/path/to/musl-gcc"; 		exit 1; 	fi
	CC=$(MUSL_CC) CGO_ENABLED=1 go build -ldflags="$(VERSION_LDFLAGS) -linkmode external -extldflags '-static' -s -w" 	  -o $(DESTDIR)/cacheman shared/cacheman/cmd/cacheman/cacheman.go

# This rule is the can-do-it-all binary
abfs: maybe-copy-license
	go install -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/version.expiration=$(EXPIRATION_EPOCH_SEC)" \
	  cmd/abfs/abfs.go

abfs-client: maybe-copy-license
	go install -ldflags="-X github.com/google/abfs/shared/version.version=$(VERSION)-client \
	  -X github.com/google/abfs/shared/version.expiration=$(EXPIRATION_EPOCH_SEC) \
	  -X github.com/google/abfs/shared/instance.forceFlavor=abfs.client" \
	  cmd/abfs/abfs.go

abfs-pusher: maybe-copy-license
	go install -ldflags="-X github.com/google/abfs/shared/version.version=$(VERSION)-pusher \
	  -X github.com/google/abfs/shared/version.expiration=$(EXPIRATION_EPOCH_SEC) \
	  -X github.com/google/abfs/shared/instance.forceFlavor=abfs.pusher" \
	  cmd/abfs/abfs.go

build-abfs: maybe-copy-license
	go build -ldflags="$(VERSION_LDFLAGS)" cmd/abfs/abfs.go

build-abfs-dbg: maybe-copy-license
	$(info NOTE: It is recommended to 'export GODEBUG=asyncpreemptoff=1')
	go build -gcflags=all="-N -l" -ldflags="$(VERSION_LDFLAGS)" cmd/abfs/abfs.go

ninjago:
#ninjago has its own version concept so we don't pass the VERSION_LDFLAGS
	go install -ldflags="-X github.com/google/abfs/shared/instance.forceFlavor=abfs.ninjago" \
	  cmd/ninjago/ninjago.go

abfsbox:
	go build -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/instance.forceFlavor=abfs.abfsbox" \
	  -o $(DESTDIR)/abfsbox cmd/abfsbox/*.go

abfs-bazel-daemon:
	go build -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/instance.forceFlavor=abfs.bazel-daemon" \
	  -o $(DESTDIR)/abfs-bazel-daemon cmd/abfs-bazel-daemon/abfs-bazel-daemon.go

abfs-wrap-no-cgo:
	CGO_ENABLED=0 go build -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/instance.forceFlavor=abfs.wrap" \
	  -o $(DESTDIR)/abfs-wrap-no-cgo cmd/abfs-wrap/abfs-wrap.go

tap-no-cgo:
	CGO_ENABLED=0 go build -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/instance.forceFlavor=abfs.tap" \
	  -o $(DESTDIR)/tap-no-cgo cmd/tap/tap.go

rbetap:
	go build -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/instance.forceFlavor=abfs.rbetap" \
	  -o $(DESTDIR)/rbetap cmd/rbetap/main.go

rbetap-no-cgo:
	CGO_ENABLED=0 go build -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/instance.forceFlavor=abfs.rbetap" \
	  -o $(DESTDIR)/rbetap-no-cgo cmd/rbetap/main.go

abfs-rbe:
	go build -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/instance.forceFlavor=abfs.rbe" \
	  -o $(DESTDIR)/abfs-rbe ./integration/rbe/*.go

ninjago-no-cgo:
	CGO_ENABLED=0 go build -ldflags="-X github.com/google/abfs/shared/instance.forceFlavor=abfs.ninjago" \
	  -o $(DESTDIR)/ninjago-no-cgo cmd/ninjago/ninjago.go

abfsbox-no-cgo:
	CGO_ENABLED=0 go build -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/instance.forceFlavor=abfs.abfsbox" \
	  -o $(DESTDIR)/abfsbox-no-cgo cmd/abfsbox/*.go

abfs-cmd-map-no-cgo:
	CGO_ENABLED=0 go build -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/instance.forceFlavor=abfs.cmd-map" \
	  -o $(DESTDIR)/abfs-cmd-map-no-cgo cmd/abfs-cmd-map/abfs-cmd-map.go

abfs-bazel-daemon-no-cgo:
	CGO_ENABLED=0 go build -ldflags="$(VERSION_LDFLAGS) -X github.com/google/abfs/shared/instance.forceFlavor=abfs.bazel-daemon" \
	  -o $(DESTDIR)/abfs-bazel-daemon-no-cgo cmd/abfs-bazel-daemon/abfs-bazel-daemon.go

# Set SKIP_SETCAP=1 to skip granting permissions for binaries (e.g. when building as part of a
# package).
# getcap and setcap are usually installed in /usr/sbin or /sbin, which aren't on
# a normal user's PATH on some distributions (e.g. Debian), so look there too.
GETCAP ?= $(shell PATH="$$PATH:/usr/sbin:/sbin" command -v getcap)
SETCAP ?= $(shell PATH="$$PATH:/usr/sbin:/sbin" command -v setcap)

# Fail if getcap or setcap is missing
define check-setcap
@if [[ -z "$(GETCAP)" || -z "$(SETCAP)" ]]; then \
	echo "ERROR: getcap/setcap not found in PATH, /usr/sbin or /sbin." >&2; \
	echo "They are needed to grant file capabilities to $@." >&2; \
	echo "Install them ('sudo apt install libcap2-bin' on Debian/Ubuntu) and run make again," >&2; \
	echo "or set SKIP_SETCAP=1 to skip granting capabilities." >&2; \
	exit 1; \
fi
endef

casfs-mounter:
	go install -ldflags="$(VERSION_LDFLAGS)" cmd/casfs-mounter/casfs-mounter.go
ifeq ($(SKIP_SETCAP), )
	$(check-setcap)
	@"$(GETCAP)" $(DESTDIR)/casfs-mounter | grep --quiet --ignore-case "cap_sys_admin=ep" || \
		(echo "casfs-mounter needs CAP_SYS_ADMIN permissions, so you might be asked for a sudo password here." && \
		sudo "$(SETCAP)" CAP_SYS_ADMIN,CAP_SYS_MODULE+ep $(DESTDIR)/casfs-mounter)
endif

casfuse-mounter:
	go install -ldflags="$(VERSION_LDFLAGS)" cmd/casfuse-mounter/casfuse-mounter.go
ifeq ($(SKIP_SETCAP), )
	$(check-setcap)
	@"$(GETCAP)" $(DESTDIR)/casfuse-mounter | grep --quiet --ignore-case "cap_sys_resource=ep" || \
		(echo "casfuse-mounter needs CAP_SYS_RESOURCE permissions, so you might be asked for a sudo password here." && \
		sudo "$(SETCAP)" CAP_SYS_RESOURCE+ep $(DESTDIR)/casfuse-mounter)
endif

abfs-resources:
	go install cmd/abfs-resources/abfs-resources.go
ifeq ($(SKIP_SETCAP), )
	$(check-setcap)
	@"$(GETCAP)" $(DESTDIR)/abfs-resources | grep --quiet --ignore-case "cap_dac_override,cap_syslog=ep" || \
		(echo "abfs-resources needs CAP_DAC_OVERRIDE and CAP_SYSLOG permissions, so you might be asked for a sudo password here." && \
		sudo "$(SETCAP)" cap_dac_override,cap_syslog+ep $(DESTDIR)/abfs-resources)
endif

install: all

# Client uses all binaries.
install-client: abfs-client all-non-abfs

# Pusher only uses abfs binary
install-pusher: abfs-pusher

# Server only uses abfs binary
install-server: abfs

clean:
	rm -f $(DESTDIR)/abfs \
	$(DESTDIR)/casfs-mounter $(DESTDIR)/casfuse-mounter \
	$(DESTDIR)/tap-no-cgo \
	$(DESTDIR)/abfsbox $(DESTDIR)/abfsbox-no-cgo \
	$(DESTDIR)/ninjago $(DESTDIR)/ninjago-no-cgo \
	$(DESTDIR)/abfs-wrap-no-cgo $(DESTDIR)/abfs-cmd-map-no-cgo \
	$(DESTDIR)/abfs-bazel-daemon $(DESTDIR)/abfs-bazel-daemon-no-cgo \
	$(DESTDIR)/abfs-rbe $(DESTDIR)/cacheman $(DESTDIR)/abfs-resources \
	$(DESTDIR)/rbetap $(DESTDIR)/rbetap-no-cgo
