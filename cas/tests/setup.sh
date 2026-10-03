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

get_mnt_dir()
{
  echo $(mktemp -d /tmp/casfs_test.XXXXXXXXXX)
}
get_cache_dir()
{
  echo $(mktemp -d /tmp/casfs_test_cache.XXXXXXXXXX)
}
kmod_mount()
{
  echo "Mounting $1 with cache $2"

  mkdir -p "$MNT_DIR"

  sudo mount -t casfs none "$1" -o cache="$2"  -o local -o scratch="test"
  if [ $? -ne 0 ]; then
    echo "Failed to mount $1 with cache $2"
    return 1
  fi

  return $?
}

kmod_unmount()
{
  echo "Unmounting $1"
  sudo umount "$1"
  return $?
}

fuse_mount()
{
  echo "Mounting fuse $1 with cache $2"
  mkdir -p "$MNT_DIR"
  #TODO: this requires casfuse mount to be in user path, once it is added as a submodule
  # this should be fixed
  umask 0
  casfuse --local --cache "$2" --scratch="test"  mount --mount "$1" &
  if [ $? -ne 0 ]; then
    echo "Failed to mount fuse $1 with cache $2"
    return 1
  fi

  return $?
}
fuse_unmount() {
  echo "Unmounting fuse $1"
  fusermount -uz "$1"
}

mount()
{
  if [[ -v use_fuse_mount ]]; then
    fuse_mount "$1" "$2"
  else
    kmod_mount "$1" "$2"
  fi
}

unmount() {
  if [[ -v use_fuse_mount ]]; then
    fuse_unmount "$1"
  else
    kmod_unmount "$1"
  fi
}

cleanup_mount()
{
  if [ -z "$1" ]; then
    echo "Invalid mount"
    return 1
  fi
  rm -r "$1"
}

