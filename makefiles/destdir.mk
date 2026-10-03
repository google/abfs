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

# We want DESTDIR to be configurable.
ifeq ($(DESTDIR), )

# If it's not set, then we'll respect golang's behavior.
# Go installs will go into GOBIN if defined, otherwise GOPATH/bin.
DESTDIR := $(shell go env GOBIN)
ifeq ($(DESTDIR), )
DESTDIR := $(shell go env GOPATH)/bin
endif

else

# If DESTDIR was configured by a caller, we must set GOBIN there for
# 'go install'.
export GOBIN := $(DESTDIR)

endif #ifeq ($(DESTDIR), )

