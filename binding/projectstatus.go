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
	"time"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const ProjectStatusRef = "abfs/project-status"

const (
	CurrentProjectStatusVersion = "0.2"
	ObjectTypeProjectStatus     = "project-status"
)

type ProjectStatus struct {
	JSONBaseObject
	Version     string
	Pusher      string
	LFS         bool
	LastSuccess time.Time
	LastUpdate  time.Time
	LastError   string
}

func init() {
	objects.Factory.Register(ObjectTypeProjectStatus, func() Object {
		return new(ProjectStatus)
	})
}

func (p *ProjectStatus) Encode() error {
	return ObjectJSONEncode(p, ObjectTypeProjectStatus)
}

func (p *ProjectStatus) Decode(hash Hash, content []byte) error {
	return ObjectJSONDecode(p, ObjectTypeProjectStatus, hash, content)
}

func (p *ProjectStatus) Links() []Hash {
	return nil
}

func FetchProjectStatusFromStore(ctx context.Context, server, project string, store entities.CASStore) (*ProjectStatus, error) {
	results, err := store.GetRefs(server, project, []string{ProjectStatusRef})
	if entities.IsNotExist(err) || err == nil && results[0].IsZero() {
		return &ProjectStatus{
			Version: CurrentProjectStatusVersion,
		}, nil
	}
	if err != nil {
		return nil, err
	}

	return FetchObject[ProjectStatus](ctx, store, results[0])
}
