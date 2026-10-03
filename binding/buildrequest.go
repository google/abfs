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
	"fmt"
	"time"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const (
	CurrentBuildRequestVersion            = "0.1"
	ObjectTypeBuildRequest     ObjectType = "build-request"
)

type BuildRequest struct {
	JSONBaseObject
	Version            string
	RequestCtx         BuildRequestCtx
	RefPrefix          string
	PinnedManifestHash Hash
	BuildCommand       string
	NSJailBuild        NSJailBuild
}

func init() {
	objects.Factory.Register(ObjectTypeBuildRequest, func() Object {
		return new(BuildRequest)
	})
}

type BuildRequestCtx struct {
	RequestTime      time.Time
	RequestTimestamp int64
	UserName         string
	HostName         string
}

type NSJailBuild struct {
	Enabled  bool
	Overlays []string
}

func (e *BuildRequest) Encode() error {
	return ObjectJSONEncode(e, ObjectTypeBuildRequest)
}

func (e *BuildRequest) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(e, ObjectTypeBuildRequest, hash, content)
}

func (e *BuildRequest) Links() []Hash {
	var links []Hash
	if !e.PinnedManifestHash.IsZero() {
		links = append(links, e.PinnedManifestHash)
	}
	return links
}

const (
	CurrentBuildResultVersion            = "0.2"
	ObjectTypeBuildResult     ObjectType = "build-result"
)

type BuildResult struct {
	JSONBaseObject
	Version        string
	BuildRequest   Hash
	MountDetails   Hash
	BuildHashes    []BuildHash
	UnmountDetails Hash
	// UploadSecs is how long it took the cache to upload everything after the build completed, or the timeout value if waiting timed out
	UploadSecs float64
	Err        string
}

func init() {
	objects.Factory.Register(ObjectTypeBuildResult, func() Object {
		return new(BuildResult)
	})
}

type BuildHash struct {
	BuildStart Hash
	BuildInfo  Hash
}

func (e *BuildResult) Encode() error {
	return ObjectJSONEncode(e, ObjectTypeBuildResult)
}

func (e *BuildResult) Decode(hash Hash, content []byte) error {
	t, buf, err := entities.DecodeObject(content)
	if err != nil {
		return err
	}
	if t != string(ObjectTypeBuildResult) {
		return fmt.Errorf("expected a %s object but got %s: %w", ObjectTypeBuildResult, t, entities.ErrObjectParse)
	}
	var temp struct {
		Version string
	}
	if err := json.Unmarshal(buf, &temp); err != nil {
		return err
	}
	if temp.Version == OldBuildResult01Version {
		br := &OldBuildResult01{}
		if err := json.Unmarshal(buf, br); err != nil {
			return err
		}
		br.ToBuildResult(e)

		// in this case, the content and hash won't match since we are changing
		// the structure
	} else {
		if err := json.Unmarshal(buf, e); err != nil {
			return err
		}

		e.Base().Encoded = content
		e.Base().Hash = hash
	}

	return nil
}

func (e *BuildResult) Links() []Hash {
	var links []Hash
	if !e.BuildRequest.IsZero() {
		links = append(links, e.BuildRequest)
	}
	if !e.MountDetails.IsZero() {
		links = append(links, e.MountDetails)
	}
	for _, bh := range e.BuildHashes {
		if !bh.BuildStart.IsZero() {
			links = append(links, bh.BuildStart)
		}
		if !bh.BuildInfo.IsZero() {
			links = append(links, bh.BuildInfo)
		}
	}
	if !e.UnmountDetails.IsZero() {
		links = append(links, e.UnmountDetails)
	}
	return links
}

const OldBuildResult01Version = "0.1"

// OldBuildResult01 was the format of the struct at version 0.1
type OldBuildResult01 struct {
	JSONBaseObject
	Version        string
	BuildRequest   Hash
	MountDetails   Hash
	BuildInfos     []Hash
	UnmountDetails Hash
	Err            string
}

func (br *OldBuildResult01) ToBuildResult(e *BuildResult) {
	e.Version = CurrentBuildResultVersion
	e.BuildRequest = br.BuildRequest
	e.MountDetails = br.MountDetails
	e.UnmountDetails = br.UnmountDetails
	e.Err = br.Err
	e.BuildHashes = make([]BuildHash, len(br.BuildInfos))
	for i, bi := range br.BuildInfos {
		e.BuildHashes[i] = BuildHash{BuildInfo: bi}
	}
}
