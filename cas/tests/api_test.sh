#! /usr/bin/env bash

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

set -e

source $(dirname "$0")/setup.sh

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
MNT_DIR=$(get_mnt_dir)
CACHE_DIR=$(get_cache_dir)
NON_CAS_DIR=$(mktemp -d /tmp/non-casfs_test.XXXXXXXXXX)
do_cleanup=1

cleanup()
{
  if [ $do_cleanup == 1 ]; then
    unmount $MNT_DIR
    rm -rf $CACHE_DIR
    rmdir $MNT_DIR
    rm -rf $NON_CAS_DIR
  fi
}

#trap "cleanup" EXIT
mount $MNT_DIR $CACHE_DIR
if [ $? -ne 0 ]; then
  exit $?
fi

run_api_tests()
{
  umask 0

  local extra_args=""
  if [[ ! -v use_fuse_mount ]]; then
    extra_args="-arisDir $CACHE_DIR/aris"
  fi
  go test -C $ROOT_DIR/fs/tests/ -v  -args -root $MNT_DIR  -objectsDir $CACHE_DIR/objects $extra_args -nonCasDir $NON_CAS_DIR -cli $ROOT_DIR/cmd/fs/casfs --cacheDir $CACHE_DIR --scratchDir="test"


}

run_api_tests
api_test_ret=$?

cleanup

exit $api_test_ret


