// Copyright 2025 Google LLC
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

package hash

import "io"

// Hash object used in the CAS library.
type Hash interface {
	String() string               // Return string representation of the hash
	IsEmptyDir() bool             // Compare against empty directory hash
	IsEmptyFile() bool            // Compare against empty file hash
	IsZero() bool                 // Compare against a zero hash
	Equals(rhs Hash) bool         // Compare two hash values
	Read(io.Reader) (int, error)  // Read the hash data from input
	Write(io.Writer) (int, error) // Write the hash data from input
	Data() []byte                 // Access raw data of hash
}

// Create hash from data
type HashFunc interface {
	io.Writer        // Write data to the hash function
	Sum([]byte) Hash // Append the input data and return Hash for the data
	Size() int       // Size of the hash
}

// Hasher used to create new hashes or hash function objects.
type Hasher interface {
	NewHash(h string) (Hash, error) // Create new hash based on string representation
	ZeroHash() Hash                 // Create a zero hash
	EmptyDirHash() Hash             // Create empty dir hash
	EmptyFileHash() Hash            // Create empty file hash
	New() HashFunc                  // Create new hasher function to create a hash
}
