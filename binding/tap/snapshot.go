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

package tap

import (
	"log"
	"syscall"

	"github.com/google/abfs/shared/entities"
)

type snapshot struct {
	prefix   string
	hashStat func(path string) (*entities.HashStat, error)
}

func (s *snapshot) Snapshot(path string) (*entities.PatchEntry, error) {
	relPath := entities.ChrootTransform(path, s.prefix)
retry:
	hs, err := s.hashStat(relPath)
	if err != nil {
		// Add test case to cover this
		if err == syscall.EINTR {
			goto retry
		}
		log.Printf("Error hashing %s:%s\n", relPath, err)
		return nil, err
	}
	return &entities.PatchEntry{
		Path: path,
		Mode: hs.Mode,
		Hash: hs.Hash,
		Size: hs.Size}, nil

}
