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
	"context"
	"testing"

	"github.com/google/abfs/shared/entities"
)

type dummyRequest struct {
	tag string
}

func (r *dummyRequest) Links(accessor ObjectAccessor) ([]entities.Hash, error) {
	return nil, nil
}
func (r *dummyRequest) Execute(ctx context.Context, updater ABFSUpdater, accessor ObjectAccessor) error {
	return nil
}
func (r *dummyRequest) Type() string {
	return "object"
}
func (r *dummyRequest) String() string {
	return r.tag
}
func (r *dummyRequest) Key() entities.Hash {
	return entities.Hash{}
}

// TestPreventStall_HandleRequestLinked is a regression test to catch a previous stall bug where dagSync
// would spin at the head of the queue
func TestPreventStall_HandleRequestLinked(t *testing.T) {
	d := New(&DagSyncOpts{
		WorkersCount: 0, // prevent any background execution
	})

	reqN := &dummyRequest{tag: "n"}
	reqM := &dummyRequest{tag: "m"}

	d.lock.Lock()
	n := d.node(reqN)
	m := d.node(reqM)

	// Set up n depending on m. Since m is in HolderState (unlinked), n.degree remains 0.
	d.addDependency(n, m)

	// Activate n, sending it to QueuedState since its degree is 0.
	d.activate(n)

	if n.state != QueuedState {
		t.Fatalf("expected n to be in QueuedState, got %s", n.state)
	}
	d.lock.Unlock()

	// Simulate m finishing link building by directly passing a requestLinked message to handle().
	// This executes synchronously under lock inside handle().
	d.handle(&requestLinked{node: m, links: nil, err: nil})

	d.lock.Lock()
	defer d.lock.Unlock()

	// With the prevention fix, n should have automatically transitioned to BlockedState
	// when its degree became > 0. Without the fix, it would remain in QueuedState with degree > 0.
	if n.degree == 0 {
		t.Fatalf("expected n.degree > 0, got %d", n.degree)
	}
	if n.state == QueuedState {
		t.Fatalf("prevention failed: n remained in QueuedState with degree %d", n.degree)
	}
	if n.state != BlockedState {
		t.Fatalf("expected n to transition to BlockedState, got %s", n.state)
	}
}

func TestPreventStall_AddDependency(t *testing.T) {
	d := New(&DagSyncOpts{
		WorkersCount: 0,
	})

	reqN := &dummyRequest{tag: "n"}
	reqM := &dummyRequest{tag: "m"}

	d.lock.Lock()
	defer d.lock.Unlock()

	n := d.node(reqN)
	m := d.node(reqM)

	// Send n to QueuedState
	d.activate(n)
	if n.state != QueuedState {
		t.Fatalf("expected n to be in QueuedState, got %s", n.state)
	}

	// Manually set m to a linked state, e.g. BlockedState
	d.transit(m, BlockedState)

	// Now call addDependency(n, m). Since m is linked, n.degree increments.
	// With the fix, n should immediately transition to BlockedState.
	d.addDependency(n, m)

	if n.degree == 0 {
		t.Fatalf("expected n.degree > 0, got %d", n.degree)
	}
	if n.state == QueuedState {
		t.Fatalf("prevention failed: n remained in QueuedState with degree %d", n.degree)
	}
	if n.state != BlockedState {
		t.Fatalf("expected n to transition to BlockedState, got %s", n.state)
	}
}
