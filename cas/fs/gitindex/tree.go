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

package gitindex

import "context"

// FileEntry represents a file, symlink, or submodule in the tree.
type FileEntry struct {
	Name string
	Sha  [20]byte
	Mode uint32
	Size uint32
}

// TreeDirectory represents a directory node in the initial state tree.
type TreeDirectory interface {
	Name() string
	Sha() [20]byte
	Files(ctx context.Context) ([]FileEntry, error)
	Subtrees(ctx context.Context) ([]TreeDirectory, error)
}
