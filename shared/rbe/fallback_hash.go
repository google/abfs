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

package rbe

import (
	"crypto/sha1"

	"github.com/google/abfs/shared/entities"
)

// fallbackFileHash generates a deterministic fake SHA-1 from the RBE SHA-256 for a file.
// It prefixes the hash with "file:" to prevent collisions with directories.
func fallbackFileHash(rbeHash string) entities.Hash {
	h := sha1.Sum([]byte("file:" + rbeHash))
	return entities.Hash(h)
}

// fallbackDirHash generates a deterministic fake SHA-1 from the RBE SHA-256 for a directory.
// It prefixes the hash with "dir:" to prevent collisions with files.
func fallbackDirHash(rbeHash string) entities.Hash {
	h := sha1.Sum([]byte("dir:" + rbeHash))
	return entities.Hash(h)
}

// fallbackSymlinkHash generates a deterministic fake SHA-1 from the symlink target.
// It prefixes the target with "symlink:" to prevent collisions.
func fallbackSymlinkHash(target string) entities.Hash {
	h := sha1.Sum([]byte("symlink:" + target))
	return entities.Hash(h)
}
