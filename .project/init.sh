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

#
# Run one time project level initialization after cloning the
# repository.
#
basename=`dirname $0`
top_level=`git rev-parse --show-toplevel`
if [ $? -ne 0 ]; then
  echo $0 " script is to be run inside project repository"
  exit -1
fi

# Install GIT hooks
echo "Installing git hooks "
cp -vr $basename/hooks/* $top_level/.git/hooks/.

