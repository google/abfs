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

package protocol

import (
	"time"

	"github.com/google/abfs/shared/entities"
)

type GetRefsReq struct {
	Server  string
	Project string
	Refs    []string
}

type RefHash struct {
	Ref  string
	Hash string
}

type PutRefsReq struct {
	Server   string
	Project  string
	Refs     []RefHash
	Blocking bool
}

type SearchRefsReq struct {
	Server    string
	Project   string
	RefPrefix string
	Limit     int
}

type ProjectRefHash struct {
	Project string
	Ref     string
	Hash    string
}

type CleanReq struct {
	Blobs   bool
	Objects bool
	Hashes  []string
}

type NewMapMessage struct {
	From      entities.Hash
	To        entities.Hash
	LocalOnly bool
}

type ExecuteRequest struct {
	EvalRequest    entities.Hash
	Patches        []entities.Hash
	StoreMapResult bool
	PrefetchInputs bool
}

type WaitForRequest struct {
	TimeoutSecs float64 // Zero means no timeout.
	Priority    float64
	Objects     []entities.Hash
}

type IdentityPutReq struct {
	SHA1   entities.Hash
	SHA256 string
	Size   int64
	Type   int // 0: File, 1: Symlink, 2: Directory
}

type IdentityGetReq struct {
	SHA1 entities.Hash
}

type IdentityGetResp struct {
	Found  bool
	SHA256 string
	Size   int64
	Type   int // 0: File, 1: Symlink, 2: Directory
}

type PrefetchRequest struct {
	Blobs    []entities.Hash
	Objects  []entities.Hash
	Priority float64
}

type PushBuildMetricsReq struct {
	AndroidTarget     string
	BuildGoal         string
	Status            string
	BuildDurationSecs float64
	HasNinjaRun       bool
	CacheHitRatio     float64
	CacheHitCount     uint32
	CacheMissCount    uint32
	Phases            map[string]float64
}

type HTTP interface {
	GetRefs(server string, project string, refs []string) ([]entities.Hash, error)
	PutRefs(server string, project string, refs []entities.RefHash, blocking bool) error
	SearchRefs(server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error)
	UseMany(hashes []entities.Hash) error
	WaitIdle(timeout time.Duration) error
	Clean(req CleanReq) error
	SendNewMap(from, to entities.Hash, localOnly bool) error
	FetchMapRequest(key entities.Hash) error
	MapResults(from entities.Hash) ([]entities.Hash, error)
	Execute(req entities.Hash, patches []entities.Hash, storeMapResult, PrefetchInputs bool) (resp entities.Hash, err error)
	WaitFor(timeout time.Duration, priority float64, objects []entities.Hash) (err error)
	Stats() (CacheManStats, error)
	KeepAlive(key entities.Hash) error
	IsObjectRemote(key entities.Hash) (bool, error)
	StartGC() error
	Prefetch(blobs, objects []entities.Hash, priority float64) error
	PushBuildMetrics(req *PushBuildMetricsReq) error
}
