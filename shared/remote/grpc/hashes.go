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

package grpc

import (
	"github.com/google/abfs/shared/entities"
)

type Hash = entities.Hash
type SHA256 = entities.SHA256

func HashesToHashBytes(hashes []Hash) [][]byte {
	bytesArray := make([][]byte, len(hashes))
	flatBytes := make([]byte, len(hashes)*20)
	for i, hash := range hashes {
		start := i * 20
		end := start + 20
		copy(flatBytes[start:end], hash[:])
		bytesArray[i] = flatBytes[start:end]
	}
	return bytesArray
}

func HashBytesToHashes(bytes [][]byte) []Hash {
	results := make([]Hash, len(bytes))
	for i, h := range bytes {
		copy(results[i][:], h)
	}
	return results
}

func SHA256BytesToSHA256(bytes [][]byte) []SHA256 {
	shas := make([]SHA256, len(bytes))
	for i, h := range bytes {
		copy(shas[i][:], h)
	}
	return shas
}

func HashToBytes(hash Hash) []byte {
	return hash[:]
}

func HashFromInputBytes(input []byte) (hash Hash) {
	copy(hash[:], input)
	return
}

func HashesFromInputBytes(input [][]byte) []Hash {
	hashes := make([]Hash, len(input))
	for i, in := range input {
		copy(hashes[i][:], in)
	}
	return hashes
}

func OutputBytesFromHashes(hashes []Hash) [][]byte {
	bytesArray := make([][]byte, len(hashes))
	flatBytes := make([]byte, len(hashes)*entities.HashSize)
	for i, hash := range hashes {
		start := i * entities.HashSize
		end := start + entities.HashSize
		copy(flatBytes[start:end], hash[:])
		bytesArray[i] = flatBytes[start:end]
	}
	return bytesArray
}

func OutputBytesFromSHA256(shas []SHA256) [][]byte {
	bytesArray := make([][]byte, len(shas))
	flatBytes := make([]byte, len(shas)*entities.SHA256Size)
	for i, sha := range shas {
		start := i * entities.SHA256Size
		end := start + entities.SHA256Size
		copy(flatBytes[start:end], sha[:])
		bytesArray[i] = flatBytes[start:end]
	}
	return bytesArray
}
