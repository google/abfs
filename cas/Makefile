# Copyright 2025 Google LLC
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

GOAPPS := cmd/cas cmd/fs/casfs

.PHONY: $(GOAPPS) test

all: $(GOAPPS)

cmd/cas:
	go -C cmd build -o cas

cmd/fs/casfs:
	go -C cmd/fs build -o casfs


# TODO: add coverage across both cas library and fs/api

test:
	go test ./cas/... --count=1
	go test ./fs/gitindex/... --count=1
	go test -c ./fs/tests

fs_api_test: test
	./tests/api_test.sh

fs_fuse_api_test: test
	use_fuse_mount=1 ./tests/api_test.sh
