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

package pt

// Provide pass through blob support, the pt package uses a pt.store to create a
// backend directory and will store all files in this single directory with random
// file names.

import (
	"os"

	"github.com/google/abfs/casfuse/fs"
)

type store struct {
	baseDir string
}

func (s *store) Create() fs.Blob {
	return &blob{fd: -1, baseDir: s.baseDir}
}

func NewBlobStore(baseDir string) (*store, error) {
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, err
	}
	return &store{baseDir: baseDir}, nil

}
