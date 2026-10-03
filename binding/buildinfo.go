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
	"encoding/json"
	"os"
	"path"
	"time"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const (
	CurrentBuildInfoVersion            = "0.1"
	ObjectTypeBuildStart    ObjectType = "build-start"
	ObjectTypeBuildInfo     ObjectType = "build-info"
)

// BuildStartData is embedded in the BuildStart and BuildInfo objects. The reason that BuildStartData
// needed to be extracted out was that both BuildStart and BuildInfo implement JSONBaseObject and it
// is better to not to hide methods like Encode() or Links().
type BuildStartData struct {
	BuildID       string
	MountDetails  string
	RootBegin     string
	Start         time.Time
	PreviousBuild string
	OutDir        string
	BuildCommand  []string
}

type BuildStart struct {
	JSONBaseObject
	Version string
	BuildStartData
}

func init() {
	objects.Factory.Register(ObjectTypeBuildStart, func() Object {
		return new(BuildStart)
	})
}

func (e *BuildStart) Encode() error {
	return ObjectJSONEncode(e, ObjectTypeBuildStart)
}

func (e *BuildStart) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(e, ObjectTypeBuildStart, hash, content)
}

func (e *BuildStart) Links() []Hash {
	// TODO: Add links
	return []Hash{}
}

var _ Object = (*BuildStart)(nil)

func ReadBuildStart(chroot, outDir string) (bs *BuildStart, retErr error) {
	file, err := os.Open(path.Join(chroot, outDir, ".build_start"))
	if err != nil {
		return nil, err
	}
	defer func() { retErr = entities.FirstError(retErr, file.Close()) }()
	b := new(struct {
		*BuildStart
		Hash string
	})
	if err := json.NewDecoder(file).Decode(b); err != nil {
		return nil, err
	}
	b.BuildStart.Hash = entities.NewHash(b.Hash)
	return b.BuildStart, nil
}

type NinjaFailure struct {
	Targets []string
	Req     string
	Resp    string
	Error   string `json:",omitempty"`
}

type NinjaStats struct {
	GoalTargets        []string
	StartTime          time.Time
	RunSecs            float64
	ParseSecs          float64
	LoadNinjaFilesSecs float64
	LoadFilesSecs      float64
	BuildGraphSecs     float64
	InitStatesSecs     float64
	EvalFilesSecs      float64
	EvalNodesSecs      float64

	GraphNodesCount int
	GraphFilesCount int
	Failures        []NinjaFailure

	NodesCount      uint32
	DoneCount       uint32
	FailCount       uint32
	SkipCount       uint32
	CacheHitCount   uint32
	CacheMissCount  uint32
	CacheForceCount uint32
	CacheHitPercent float64

	NinjaEntryFile string
	BuildFiles     []string `json:",omitempty"`

	Error string
}

var _ Object = (*BuildInfo)(nil)

type NinjaRunInfo struct {
	*NinjaStats
	BuildsFiles map[string]string
}

type BuildInfo struct {
	JSONBaseObject
	Version string
	BuildStartData
	BuildStart    string
	RootEnd       string
	End           time.Time
	Error         string
	NinjaLog      string
	WrapLog       string
	CmdMapLog     string
	MismatchesLog string
	NinjaRunInfo  []NinjaRunInfo
}

func init() {
	objects.Factory.Register(ObjectTypeBuildInfo, func() Object {
		return new(BuildInfo)
	})
}

func (e *BuildInfo) Encode() error {
	return ObjectJSONEncode(e, ObjectTypeBuildInfo)
}

func (e *BuildInfo) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(e, ObjectTypeBuildInfo, hash, content)
}

func (e *BuildInfo) Links() []Hash {
	// TODO: Add links
	return []Hash{}
}
