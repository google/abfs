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
	"slices"
	"time"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

type WorkspaceStatus struct {
	Name          string
	Path          string
	Conflicts     []string
	LastSyncError string
	LastSyncStart time.Time
	LastSyncEnd   time.Time
	LastUpdate    time.Time
	LastSnapshot  entities.Hash
}

type GitRepoSnapshot struct {
	WorkDir     string
	GitDir      string
	ObjectsDir  string
	WorkDirHash entities.Hash
	PacksHash   entities.Hash
	GitDirHash  entities.Hash
	IndexHash   entities.Hash
}

type Snapshot struct {
	Root      entities.Hash
	GitRepos  []GitRepoSnapshot
	Conflicts []string
}

func (s Snapshot) Equals(snapshot Snapshot) bool {
	return s.Root == snapshot.Root &&
		slices.Equal(s.GitRepos, snapshot.GitRepos) &&
		slices.Equal(s.Conflicts, snapshot.Conflicts)
}

type MountDaemon interface {
	Snapshot(ctx context.Context) (Snapshot, error)
	Restore(ctx context.Context, snapshot Snapshot) error
	AddGitRepo(workTree string, repo GitRepo)
	GetGitRepo(workTree string) (GitRepo, bool)
	GetMountDetails() *MountDetails
}

type WSSnapshot struct {
	JSONBaseObject
	Version      string
	MountDetails entities.Hash
	Time         time.Time
	Snapshot     Snapshot
}

const (
	CurrentWSSnapshotVersion                     = "0.1"
	ObjectTypeWSSnapshot     entities.ObjectType = "ws-snapshot"
)

func init() {
	objects.Factory.Register(ObjectTypeWSSnapshot, func() Object {
		return new(WSSnapshot)
	})
}

func (s *WSSnapshot) Encode() error {
	return ObjectJSONEncode(s, ObjectTypeWSSnapshot)
}

func (s *WSSnapshot) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(s, ObjectTypeWSSnapshot, hash, content)
}

func (s *WSSnapshot) Links() []Hash {
	links := make([]Hash, 0, 2+4*len(s.Snapshot.GitRepos))

	if !s.MountDetails.IsZero() {
		links = append(links, s.MountDetails)
	}

	links = append(links, s.Snapshot.Root)

	for _, g := range s.Snapshot.GitRepos {
		links = append(links, g.WorkDirHash, g.PacksHash, g.GitDirHash, g.IndexHash)
	}

	return links
}

type IndexSnapEntry struct {
	Name string
	Hash entities.Hash
	Size int64
	Mode uint32
}

type IndexSnapObject struct {
	JSONBaseObject
	Version string
	Entries []IndexSnapEntry
}

var EmptyIndexSnapObject = &IndexSnapObject{
	Version: CurrentIndexSnapObjectVersion,
}

func init() {
	if err := EmptyIndexSnapObject.Encode(); err != nil {
		panic(err)
	}
}

const (
	CurrentIndexSnapObjectVersion                     = "0.1"
	ObjectTypeIndexSnapObject     entities.ObjectType = "index-snap-object"
)

func init() {
	objects.Factory.Register(ObjectTypeIndexSnapObject, func() Object {
		return new(IndexSnapObject)
	})
}

func (s *IndexSnapObject) Encode() error {
	return ObjectJSONEncode(s, ObjectTypeIndexSnapObject)
}

func (s *IndexSnapObject) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(s, ObjectTypeIndexSnapObject, hash, content)
}

func (s *IndexSnapObject) Links() []Hash {
	result := make([]Hash, len(s.Entries))
	for i, e := range s.Entries {
		result[i] = e.Hash
	}
	return result
}
