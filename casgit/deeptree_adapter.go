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

package casgit

import (
	"context"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/cas/fs/gitindex"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
)

type deepTreeDir struct {
	git     *CASGit
	name    string
	sha     [20]byte
	entries []*entities.DeepEntry
}

// NewDeepTreeDir creates a TreeDirectory root from a DeepTreeObject.
func NewDeepTreeDir(git *CASGit, deepTree *gitobjects.DeepTreeObject) gitindex.TreeDirectory {
	return &deepTreeDir{
		git:     git,
		name:    "",
		sha:     deepTree.Entries[0].Hash,
		entries: deepTree.Entries,
	}
}

func (d *deepTreeDir) Name() string  { return d.name }
func (d *deepTreeDir) Sha() [20]byte { return d.sha }

func (d *deepTreeDir) Files(ctx context.Context) ([]gitindex.FileEntry, error) {
	var files []gitindex.FileEntry
	for i := 1; i < len(d.entries); {
		ent := d.entries[i]
		switch ent.Mode {
		case entities.FileModeDir:
			// Skip directory spans
			i += int(ent.Size)
		case entities.FileModeDeepTree:
			i++
		default:
			var mode uint32
			switch ent.Mode {
			case entities.FileModeExecutable:
				mode = 0100755
			case entities.FileModeSymlink:
				mode = 0120000
			case entities.FileModeSubmodule:
				mode = 0160000
			default:
				mode = 0100644
			}

			files = append(files, gitindex.FileEntry{
				Name: ent.Name,
				Sha:  ent.Hash,
				Mode: mode,
				Size: uint32(ent.Size),
			})
			i++
		}
	}
	return files, nil
}

func (d *deepTreeDir) Subtrees(ctx context.Context) ([]gitindex.TreeDirectory, error) {
	var subtrees []gitindex.TreeDirectory
	for i := 1; i < len(d.entries); {
		ent := d.entries[i]
		switch ent.Mode {
		case entities.FileModeDir:
			span := int(ent.Size)
			subtrees = append(subtrees, &deepTreeDir{
				git:     d.git,
				name:    ent.Name,
				sha:     ent.Hash,
				entries: d.entries[i : i+span],
			})
			i += span
		case entities.FileModeDeepTree:
			dt, err := binding.FetchObject[gitobjects.DeepTreeObject](ctx, d.git.store, ent.Hash)
			if err != nil {
				return nil, err
			}
			subtrees = append(subtrees, &deepTreeDir{
				git:     d.git,
				name:    ent.Name,
				sha:     dt.Entries[0].Hash,
				entries: dt.Entries,
			})
			i++
		default:
			i++
		}
	}
	return subtrees, nil
}
