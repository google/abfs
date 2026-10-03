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

package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/abfs/cas/hash"
)

//Test hasher will write the data to a string representation and hash the resulting string

// Will use SHA-256 for hashing as current use case uses SHA-1 to make sure interface holds up
const (
	// hashSize is the number of bytes in a test hash.
	hashSize = 32
	// hashHexSize is the length of a test hash in hexadecimal notation.
	hashHexSize = 64
)

type ourhash [hashSize]byte

var emptyFileHash ourhash
var emptyDirHash ourhash

func init() {
	_, _ = hex.Decode(emptyFileHash[:], []byte("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"))
	_, _ = hex.Decode(emptyDirHash[:], []byte("4b825dc642cb6eb9a060e54bf8d69288fbee4904"))
}

func (h *ourhash) String() string {
	return hex.EncodeToString(h[:])
}
func (h *ourhash) IsEmptyDir() bool {
	return *h == emptyDirHash
}
func (h *ourhash) IsEmptyFile() bool {
	return *h == emptyFileHash
}
func (h *ourhash) IsZero() bool {
	return *h == ourhash{}
}
func (h *ourhash) Equals(rhs hash.Hash) bool {
	return *h == *rhs.(*ourhash)
}
func (h *ourhash) Write(w io.Writer) (int, error) {
	return w.Write(h[:])
}
func (h *ourhash) Read(r io.Reader) (int, error) {
	return io.ReadFull(r, h[:])
}
func (h *ourhash) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.String())
}
func (h *ourhash) Data() []byte {
	return h[:]
}
func hashString(input string) hash.Hash {
	data := []byte(input)

	var out ourhash
	out = sha256.Sum256(data)

	return &out
}

// no actual content for files just use the file path as the content to hash
// add seed as well to avoid clashing with symlinks
func hashFile(pathName string, sz int64) hash.Hash {
	return hashString(fmt.Sprintf("%s......CONTENT..%d", pathName, sz))
}
func hashSymLink(target string) hash.Hash {
	return hashString(target)
}
func hashDirEntries(de DirEntryReader) ([]DirEntry, hash.Hash) {
	var builder strings.Builder
	var enteries []DirEntry
	for {
		e, err := de.Read()
		if err != nil {
			break
		}
		enteries = append(enteries, *e)
		builder.WriteString(fmt.Sprintf("%s %d %s %s\n", e.Hash, e.Size, e.Mode, e.Name))
	}
	return enteries, hashString(builder.String())
}
func hashDirList(dl DirListReader) ([]DirListEntry, hash.Hash) {
	var builder strings.Builder
	var enteries []DirListEntry
	for {
		e, err := dl.Read()
		if err != nil {
			break
		}
		enteries = append(enteries, *e)
		builder.WriteString(fmt.Sprintf("%s\n", e.Name))
	}
	return enteries, hashString(builder.String())
}
func hashDeepTree(de DirEntryReader) ([]DirEntry, hash.Hash) {
	return hashDirEntries(de)
}
