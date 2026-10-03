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

package cacheman

import (
	"context"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/entities"
)

type prefetchCmd struct {
	*cachemanCmd
	*cmd.CachemanProvider
	priority    float64
	blobHexes   []string
	objectHexes []string
}

func NewPrefetchCmd(c *cachemanCmd) *prefetchCmd {
	return &prefetchCmd{cachemanCmd: c}
}

func (c *prefetchCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "prefetch",
		Usage:   "[-p <priority>] [-b <blob-hash>]... [-o <object-hash>]...",
		Aliases: []string{},
		Desc:    `Prefetches blobs and objects into the cache (it is a non-blocking command).`,
	}
}

func (c *prefetchCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.Float64VarP(&c.priority, "priority", "p", 0.5, "Priority of the prefetch request (0.0 to 1.0)")
	fl.StringSliceVarP(&c.blobHexes, "blob", "b", nil, "Blob hash to prefetch")
	fl.StringSliceVarP(&c.objectHexes, "object", "o", nil, "Object hash to prefetch")
}

func hexStrsToHashes(hexes []string) ([]entities.Hash, error) {
	hashes := make([]entities.Hash, len(hexes))
	for i, hex := range hexes {
		if !entities.IsHash(hex) {
			return nil, fmt.Errorf("invalid hash: %s", hex)
		}
		hashes[i] = entities.NewHash(hex)
	}
	return hashes, nil
}

func (c *prefetchCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	blobs, err := hexStrsToHashes(c.blobHexes)
	if err != nil {
		return err
	}
	objects, err := hexStrsToHashes(c.objectHexes)
	if err != nil {
		return err
	}

	return c.Cacheman.Prefetch(blobs, objects, c.priority)
}
