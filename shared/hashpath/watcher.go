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
	"context"
	"fmt"
	"time"

	"github.com/google/abfs/shared/entities"
)

const watchInterval = 10 * time.Second

type NewResolution func(entities.Hash, error)

// Watch does not notify if the resolved hash goes from non-zero to zero.
func Watch(ctx context.Context, hp *HashPath, store entities.CASStore, callback NewResolution) {
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()

	var prevHash entities.Hash
	res, err := Resolve(ctx, store, hp, entities.ABFSMetaServer, entities.ABFSMetaProject)
	if err != nil {
		callback(entities.ZeroHash, fmt.Errorf("failed to resolve hash path %s: %w", hp, err))
	} else {
		callback(res.Resolved, nil)
		prevHash = res.Resolved
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res, err := Resolve(ctx, store, hp, entities.ABFSMetaServer, entities.ABFSMetaProject)
			if err != nil {
				callback(entities.ZeroHash, fmt.Errorf("failed to resolve hash path %s: %w", hp, err))
				continue
			}
			if res.Resolved == prevHash {
				continue
			}
			callback(res.Resolved, nil)
			prevHash = res.Resolved
		}
	}
}
