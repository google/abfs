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

package hashpath

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/abfs/shared/entities"
)

// HashPath is a string that represents a file-system object (file or directory).
// In its simplest form a HashPath is a hash. It can also have a relative path to a given hash or reference.
// A HashPath can be resolved to a single hash of a dir or blob using a specific resolution process.
type HashPath struct {
	Hash    entities.Hash
	Ref     string
	Project string
	Server  string
	Path    string
}

func (hp *HashPath) IsSimpleHash() bool {
	return hp.Path == ""
}

func (hp *HashPath) UsesRef() bool {
	return hp.Ref != ""
}

func (hp *HashPath) String() string {
	var sb strings.Builder
	if hp.UsesRef() {
		sb.WriteString(hp.Ref)
		if hp.Project != "" {
			sb.WriteRune('@')
			sb.WriteString(hp.Project)
		}
		if hp.Server != "" {
			sb.WriteRune('@')
			sb.WriteString(hp.Server)
		}
	} else {
		sb.WriteString(hp.Hash.String())
	}

	if !hp.IsSimpleHash() {
		sb.WriteRune(':')
		sb.WriteString(hp.Path)
	}

	return sb.String()
}

// Parse a HashPath, which is a string in one of these forms:
//
//	<blob-hash>
//	<dir-hash>
//	<dir-hash>:<relative-path>
//	<commit-hash>:<relative-path>
//	<ref>:<relative-path> # with inferred project and server
//	<ref@project>:<relative-path> # with inferred server
//	<ref@project@server>:<relative-path>
//
// update README.md if a new form is added
func Parse(a string) (*HashPath, error) {
	parts := strings.SplitN(a, ":", 2)

	if len(parts) == 1 {
		if a == "" {
			return nil, fmt.Errorf("expected a hash-path but got empty string")
		}
		if !entities.IsHash(a) {
			return nil, fmt.Errorf("expected a hash when path is not used: %q", a)
		}
		return &HashPath{
			Hash: entities.NewHash(a),
		}, nil
	}

	rev, path := parts[0], parts[1]
	path = filepath.Clean(path)
	if strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("cannot accept absolute path (%q) in a hash-path", parts[1])
	}
	if strings.HasPrefix(path, "../") || path == ".." {
		return nil, fmt.Errorf("cannot accept path (%q) with parent directory in a hash-path", parts[1])
	}
	hp := &HashPath{
		Path: path,
	}

	if entities.IsHash(rev) {
		hp.Hash = entities.NewHash(rev)
		return hp, nil
	}

	parts = strings.SplitN(rev, "@", 3)
	switch len(parts) {
	case 3:
		hp.Server = parts[2]
		fallthrough
	case 2:
		hp.Project = parts[1]
		fallthrough
	case 1:
		hp.Ref = parts[0]
		if hp.Ref == "" {
			return nil, fmt.Errorf("empty revision is not supported in hash-path: %q", a)
		}
	}
	return hp, nil
}
