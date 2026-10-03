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
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/google/abfs/cas/hash"
)

// Resolve translates a given path to an DirEntry entry while resolving the symlinks on the way.
func Resolve(store StoreReader, dirHash hash.Hash, p string) (*DirEntry, error) {
	p = path.Clean(p)
	rootHash := dirHash

	var h hash.Hash = rootHash
	for i := 0; ; {
		// Fix: Check bounds before slicing.
		// If i == len(p), we've reached the end of the path.
		if i >= len(p) {
			return &DirEntry{
				Hash: h,
				Size: 0,
				Mode: FileModeDir(),
				Name: "",
			}, nil
		}

		// Fix: Only slice p[i:] if i is within bounds.
		if p[i:] == "." {
			return &DirEntry{
				Hash: h,
				Size: 0,
				Mode: FileModeDir(),
				Name: ".",
			}, nil
		}

		s := IndexSlashAfter(p, i)

		// Get the dir entries
		de, err := store.GetDirEntries(h)
		if err != nil {
			return nil, err
		}
		defer de.Close()

		found := false
		name := p[i:s]

		for {
			e, err := de.Read()
			if err != nil {
				if err == io.EOF {
					break
				}
				return nil, err
			}

			if e.Name == name {
				found = true
				if FileModeIsSymLink(e.Mode) {
					// If symlink update the path
					target, err := store.GetSymLink(e.Hash)
					if err != nil {
						return nil, err
					}
					// Re-clean the path after joining symlink target
					p = path.Clean(path.Join(p[:i], string(target), p[s:]))
					h = rootHash
					i = 0
				} else if e.Mode.IsDir() {
					// Ensure i doesn't exceed len(p) for the next iteration
					i = s + 1
					if i > len(p) {
						i = len(p)
					}
					h = e.Hash
				} else {
					// If it's a file, ensure we aren't trying to go "deeper" (e.g., file/subpath)
					if s < len(p) {
						return nil, fmt.Errorf("path %q is a file, not a directory", name)
					}
					return e, nil
				}
				break
			}
		}

		if !found {
			return nil, fmt.Errorf("path component %q not found", name)
		}
	}
}

// IndexSlashAfter returns the index of the first slash after i'th position.
// If there is no slashes, it returns the length of p.
// If return value is s, p[i:s] always is the first path segment after i.
func IndexSlashAfter(p string, i int) int {
	s := strings.IndexRune(p[i:], '/')
	if s < 0 {
		return len(p)
	}
	return s + i
}
