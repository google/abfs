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

package localcache

import (
	"fmt"
	"os"
	"path"
	"runtime"

	"golang.org/x/sync/errgroup"
)

func InitLocalCache(dir string) error {
	if err := os.MkdirAll(path.Join(dir, "tmp"), 0755); err != nil {
		return err
	}
	dirs := make(chan string)
	go func() {
		dirs <- fmt.Sprintf("%s/%s", dir, MapPrefix)
		for i := 0; i < 4096; i++ {
			dirs <- fmt.Sprintf("%s/%s%03x", dir, BlobPrefix, i)
			dirs <- fmt.Sprintf("%s/%s%03x", dir, ObjectPrefix, i)
		}
		close(dirs)
	}()

	var errs errgroup.Group
	// We need to create the prefix directories here concurrently, because if it is left uncreated,
	// the kernel module, tries to create them single-threaded which can take several seconds.
	errs.SetLimit(runtime.NumCPU())
	for dir := range dirs {
		errs.Go(func() error {
			return os.MkdirAll(dir, 0755)
		})
	}

	if err := errs.Wait(); err != nil {
		return err
	}

	return nil
}
