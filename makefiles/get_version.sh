#!/bin/bash

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

# This script returns the ABFS version with a priority system:
# 1. If the VERSION environment variable is set, that is used.
# 2. If 'git describe' returns something, use that.
# 3. Otherwise "0.0.0-dev" is used.

set -e

# From ENV variable.
if [[ -n "$VERSION" ]]; then
  echo -n "$VERSION"
  exit 0
fi

# From Git.
GIT_VERSION=$(git describe HEAD 2> /dev/null || true)
if [[ -n "$GIT_VERSION" ]]; then
  VERSION="$GIT_VERSION"

  if [[ ! "$VERSION" =~ v[0-9]+\.[0-9]+\.[0-9]+ ]]; then
    echo "Error: $VERSION does not follow semantic versioning with a leading 'v'" >&2
    exit 1
  fi

  SUFFIX=""
  if ! git diff --quiet HEAD --; then
    SUFFIX="-dirty"
  fi
  VERSION="$VERSION$SUFFIX"
  VERSION=$(echo "$VERSION" | sed 's/^v//')

  echo -n "$VERSION"
  exit 0
fi

# Fallback when no version information is available.
echo -n "0.0.0-dev"
