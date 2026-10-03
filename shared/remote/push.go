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

package remote

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

func PushFile(ctx context.Context, fileName string, remote RemoteProvider) (hash entities.Hash, size int64, retErr error) {
	f, err := os.Open(fileName)
	if err != nil {
		return entities.Hash{}, 0, err
	}
	defer func() {
		if retErr != nil {
			if err := f.Close(); err != nil {
				log.Printf("Failed to close file %s: %v", fileName, err)
			}
		}
	}()

	stat, err := f.Stat()
	if err != nil {
		return entities.Hash{}, 0, fmt.Errorf("cannot stat pack %s: %w", fileName, err)
	}
	size = stat.Size()

	hash, err = objects.HashBlobReader(size, f)
	if err != nil {
		return entities.Hash{}, 0, fmt.Errorf("cannot hash pack %s: %w", fileName, err)
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return entities.Hash{}, 0, fmt.Errorf("cannot seek to the beginning of pack %s: %w", fileName, err)
	}

	return hash, size, remote.PutObject(ctx, hash, NewBlobReader(f, size))
}
