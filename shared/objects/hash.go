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

package objects

import (
	"crypto/sha1"
	"hash"
	"io"
	"os"

	"github.com/google/abfs/shared/entities"
)

func HashFileBlob(fileName string) (entities.Hash, int64, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return entities.Hash{}, 0, err
	}
	defer entities.CloseErrCheck(file, &err)

	stat, err := file.Stat()
	if err != nil {
		return entities.Hash{}, 0, err
	}

	hash, err := HashBlobReader(stat.Size(), file)
	return hash, stat.Size(), err
}

func HashBlobReader(size int64, r io.Reader) (entities.Hash, error) {
	hasher := sha1.New()
	if err := WriteHeader(hasher, "blob", size); err != nil {
		return entities.Hash{}, err
	}

	if _, err := io.Copy(hasher, r); err != nil {
		return entities.Hash{}, err
	}

	var h entities.Hash
	copy(h[:], hasher.Sum(nil))
	return h, nil
}

func HashBlobBytes(b []byte) entities.Hash {
	hasher := sha1.New()
	if err := WriteHeader(hasher, "blob", int64(len(b))); err != nil {
		return entities.Hash{}
	}

	hasher.Write(b)

	var h entities.Hash
	copy(h[:], hasher.Sum(nil))
	return h
}

func HashBytes(b []byte) entities.Hash {
	hasher := sha1.New()

	hasher.Write(b)

	var h entities.Hash
	copy(h[:], hasher.Sum(nil))
	return h
}

func NewHasher() hash.Hash {
	return sha1.New()
}
