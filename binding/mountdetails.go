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
	"time"

	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/objects"
)

const (
	CurrentMountDetailsVersion            = "0.2"
	ObjectTypeMountDetails     ObjectType = "mount-details"
)

type MountDetails struct {
	JSONBaseObject
	Version            string
	PID                int
	StartTime          time.Time
	StartTimestamp     int64
	CmdArgs            []string
	MetaRefPrefix      string
	UserName           string
	HostName           string
	ABFSVersion        string
	ManifestHash       Hash
	PinnedManifestHash Hash
	Config             *instance.Config
	MountDuration      float64
	Error              string
}

func init() {
	objects.Factory.Register(ObjectTypeMountDetails, func() Object {
		return new(MountDetails)
	})
}

func (e *MountDetails) Encode() error {
	return ObjectJSONEncode(e, ObjectTypeMountDetails)
}

func (e *MountDetails) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(e, ObjectTypeMountDetails, hash, content)
}

func (e *MountDetails) Links() []Hash {
	var links []Hash
	if !e.ManifestHash.IsZero() {
		links = append(links, e.ManifestHash)
	}
	if !e.PinnedManifestHash.IsZero() {
		links = append(links, e.PinnedManifestHash)
	}
	return links
}

const (
	CurrentUnmountDetailsVersion            = "0.1"
	ObjectTypeUnmountDetails     ObjectType = "unmount-details"
)

type UnmountDetails struct {
	JSONBaseObject
	Version          string
	MountDetails     Hash
	MountedDuration  float64
	UnmountTime      time.Time
	ABFSKillDuration float64
	UnmountDuration  float64
	UnmountBusy      float64
}

func init() {
	objects.Factory.Register(ObjectTypeUnmountDetails, func() Object {
		return new(UnmountDetails)
	})
}

func (e *UnmountDetails) Encode() error {
	return ObjectJSONEncode(e, ObjectTypeUnmountDetails)
}

func (e *UnmountDetails) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(e, ObjectTypeUnmountDetails, hash, content)
}

func (e *UnmountDetails) Links() []Hash {
	return []Hash{e.MountDetails}
}
