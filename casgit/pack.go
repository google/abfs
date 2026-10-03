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
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

func addGitPacks(packs *binding.PackFiles, casfs bindcas.CASFS, objectsDir string) error {
	packsDir := path.Join(objectsDir, "pack")

	if err := os.MkdirAll(packsDir, 0755); err != nil {
		return err
	}

	var grp errgroup.Group
	grp.SetLimit(runtime.NumCPU())
	addFile := func(path string, h entities.Hash, size int64, packedTime time.Time) error {
		if err := casfs.Mknod(path, entities.FileModeRegular, 0); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			// continue to set hash-size
		}
		if err := casfs.SetHashSize(path, h, size); err != nil {
			return err
		}
		return os.Chtimes(path, packedTime, packedTime)
	}
	for _, pack := range packs.Packs {
		packTime := pack.PackedTime
		name := fmt.Sprintf("%d-%s-%s", packTime.Unix(), pack.Type, pack.PackID)
		grp.Go(func() error {
			return addFile(path.Join(packsDir, name+".idx"), pack.Index, pack.IndexSize, packTime)
		})
		grp.Go(func() error {
			return addFile(path.Join(packsDir, name+".pack"), pack.Content, pack.ContentSize, packTime)
		})
	}

	return grp.Wait()
}
