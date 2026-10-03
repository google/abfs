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

package dagsync

import (
	"fmt"

	"github.com/google/abfs/shared/entities"
)

type newRequest struct {
	request   Request
	priority  RequestPriority
	callbacks []Callback
}

func (m *newRequest) String() string {
	return fmt.Sprintf("{request: %s, priority=%v allbacks: %d", m.request, m.priority, len(m.callbacks))
}

type requestLinked struct {
	node    *node
	links   []entities.Hash
	err     error
	oldType string
}

func (m *requestLinked) String() string {
	return fmt.Sprintf("{node: %s, links: %s, err: %v, oldType: %s}", m.node, m.links, m.err, m.oldType)
}

type executed struct {
	node    *node
	skipped bool
	err     error
}

func (m *executed) String() string {
	return fmt.Sprintf("{node: %s, skipped: %t, err: %v}", m.node, m.skipped, m.err)
}

type retryFailedNode struct {
	node *node
}

func (m *retryFailedNode) String() string {
	return fmt.Sprintf("{node: %s", m.node)
}

type runMaintenance struct {
}

func (m *runMaintenance) String() string {
	return "{}"
}
