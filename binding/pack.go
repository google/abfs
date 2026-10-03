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

package binding

import (
	"context"
	"fmt"
	"time"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const PacksRefs = "abfs/pushed-packs"

const (
	CurrentPackFilesVersion = "0.2"
	ObjectTypePackFiles     = "pack-files"
)

type PackContentType string

const (
	CommitsPack   PackContentType = "commits"
	TreesPack     PackContentType = "trees"
	BlobsListPack PackContentType = "blobs-list"
	BlobsPack     PackContentType = "blobs"
)

type PackEntry struct {
	Type            PackContentType
	PackID          string
	PackedTime      time.Time
	PackingDuration float64
	ObjectsCount    int
	Content         entities.Hash
	ContentSize     int64
	Index           entities.Hash
	IndexSize       int64
}

type PackingStrategy string

func (s PackingStrategy) Validate() error {
	switch s {
	case PackingStrategyNone, PackingStrategyCommits, PackingStrategyTrees, PackingStrategyBlobsList, PackingStrategyBlobs:
		return nil
	default:
		return fmt.Errorf("invalid packing strategy %q", s)
	}
}

const (
	PackingStrategyNone      PackingStrategy = "none"
	PackingStrategyCommits   PackingStrategy = "commits"
	PackingStrategyTrees     PackingStrategy = "trees"
	PackingStrategyBlobsList PackingStrategy = "blobs-list"
	PackingStrategyBlobs     PackingStrategy = "blobs"
)

type PackFiles struct {
	JSONBaseObject
	Version        string
	Strategy       PackingStrategy
	LastUpdateTime time.Time
	Packs          []PackEntry
}

func init() {
	objects.Factory.Register(ObjectTypePackFiles, func() Object {
		return new(PackFiles)
	})
}

func (p *PackFiles) Encode() error {
	return ObjectJSONEncode(p, ObjectTypePackFiles)
}

func (p *PackFiles) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(p, ObjectTypePackFiles, hash, content)
}

func (p *PackFiles) Links() []Hash {
	links := make([]Hash, 0, len(p.Packs)*2)
	for _, pack := range p.Packs {
		links = append(links, pack.Content, pack.Index)
	}
	return links
}

func FetchPacksFromStore(ctx context.Context, server, project string, store entities.CASStore) (*PackFiles, error) {
	results, err := store.GetRefs(server, project, []string{PacksRefs})
	if entities.IsNotExist(err) || err == nil && results[0].IsZero() {
		return &PackFiles{
			Version: CurrentPackFilesVersion,
		}, nil
	}
	if err != nil {
		return nil, err
	}

	return FetchObject[PackFiles](ctx, store, results[0])
}
