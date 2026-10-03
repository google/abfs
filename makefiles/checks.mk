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

# Environment checks.
GO_EXIT_CODE := $(shell command -v go &> /dev/null; echo $$?)

ifneq ($(GO_EXIT_CODE), 0)
$(error go command not found, please see https://go.dev/doc/install to install and ensure it is in your path)
endif
