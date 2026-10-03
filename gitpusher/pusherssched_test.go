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

package gitpusher

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testPusher struct {
	config *ProjectConfig
	pushed chan *ProjectConfig
}

func (t *testPusher) Push(ctx context.Context) error {
	time.Sleep(10 * time.Millisecond)
	if t.pushed != nil {
		select {
		case t.pushed <- t.config:
		default:
		}
	}
	return nil
}

func (t *testPusher) Stat() PusherStat {
	panic("unimplemented")
}

func (t *testPusher) SyncDump(ctx context.Context, w io.Writer) {
	_, _ = fmt.Fprintf(w, "test-repo")
}

func (t *testPusher) SyncValidate(ctx context.Context, w io.Writer, timeout time.Duration) {
	_, _ = fmt.Fprintf(w, "test-repo")
}

func TestScheduler(t *testing.T) {
	pushed := make(chan *ProjectConfig, 10)

	sched := newPushersScheduler(0, 1, 1, 0, func(pc *ProjectConfig) Pusher {
		return &testPusher{config: pc, pushed: pushed}
	})

	err := sched.AddProject(&ProjectConfig{
		URL: "test",
		FetchConfig: FetchConfig{
			PushInterval: 10 * time.Millisecond,
		},
	}, "test-repo")
	require.NoError(t, err)

	<-pushed
	<-pushed
	require.NoError(t, sched.Stop())
}

func TestUpdateProject_NotAssigned(t *testing.T) {
	sched := newPushersScheduler(0, 2, 1, 0, func(pc *ProjectConfig) Pusher {
		return &testPusher{config: pc}
	})

	var unassignedProj *ProjectConfig
	for i := 0; i < 100; i++ {
		p := &ProjectConfig{
			Server:  "example.com",
			Project: fmt.Sprintf("proj-%d", i),
			URL:     fmt.Sprintf("example.com/proj-%d", i),
		}
		if !sched.cHasher.IsAssignedToCurrentWorker(p.Name().String()) {
			unassignedProj = p
			break
		}
	}
	require.NotNil(t, unassignedProj)

	err := sched.AddProject(unassignedProj, "test-repo")
	require.NoError(t, err)

	// Updating an unassigned project should succeed and update config without error.
	err = sched.UpdateProject(unassignedProj, "test-repo")
	require.NoError(t, err)
}

func TestUpdateProject_Assigned(t *testing.T) {
	pushed := make(chan *ProjectConfig, 10)

	sched := newPushersScheduler(0, 1, 1, 0, func(pc *ProjectConfig) Pusher {
		return &testPusher{config: pc, pushed: pushed}
	})

	proj := &ProjectConfig{
		Server:  "example.com",
		Project: "proj-assigned",
		URL:     "example.com/proj-assigned",
		FetchConfig: FetchConfig{
			PushInterval: 10 * time.Millisecond,
		},
	}

	err := sched.AddProject(proj, "test-repo")
	require.NoError(t, err)

	require.Same(t, proj, <-pushed)

	// Update project
	updatedProj := &ProjectConfig{
		Server:  "example.com",
		Project: "proj-assigned",
		URL:     "example.com/proj-assigned",
		FetchConfig: FetchConfig{
			PushInterval: 10 * time.Millisecond,
		},
	}
	err = sched.UpdateProject(updatedProj, "test-repo")
	require.NoError(t, err)

	// Wait until the updated config has pushed
	for {
		if <-pushed == updatedProj {
			break
		}
	}

	require.NoError(t, sched.Stop())
}

func TestSetIndex_Rebalance(t *testing.T) {
	sched := newPushersScheduler(0, 2, 2, 0, func(pc *ProjectConfig) Pusher {
		return &testPusher{config: pc}
	})

	proj1 := &ProjectConfig{
		Server:  "example.com",
		Project: "proj1",
		URL:     "example.com/proj1",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}
	proj2 := &ProjectConfig{
		Server:  "example.com",
		Project: "proj2",
		URL:     "example.com/proj2",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}

	require.NoError(t, sched.AddProject(proj1, "test-repo"))
	require.NoError(t, sched.AddProject(proj2, "test-repo"))

	// Switch index to 1
	sched.SetIndex(1, 2)

	// Switch index back to 0
	sched.SetIndex(0, 2)

	require.NoError(t, sched.Stop())
}

func TestSetIndex_StoppingProjectReassigned(t *testing.T) {
	projStarted := make(chan struct{}, 2)
	var (
		mu              sync.Mutex
		pusherCallCount int
	)

	sched := newPushersScheduler(0, 2, 2, 0, func(pc *ProjectConfig) Pusher {
		mu.Lock()
		pusherCallCount++
		count := pusherCallCount
		mu.Unlock()

		return &mockPusher{
			pushStarted: projStarted,
			blockPush:   count == 1,
			cancelDelay: 50 * time.Millisecond,
		}
	})

	proj := &ProjectConfig{
		Server:  "example.com",
		Project: "proj1",
		URL:     "example.com/proj1",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}

	// Ensure project is assigned to current worker
	workerIndex := 0
	if !sched.cHasher.IsAssignedToCurrentWorker(proj.Name().String()) {
		workerIndex = 1
		sched.SetIndex(1, 2)
	}
	otherWorkerIndex := 1 - workerIndex

	require.NoError(t, sched.AddProject(proj, "test-repo"))
	<-projStarted

	// Switch worker index away in background (so proj1 starts stopping)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sched.SetIndex(otherWorkerIndex, 2)
	}()

	// Small pause to allow SetIndex to mark proj1 as stopping
	time.Sleep(10 * time.Millisecond)

	// Immediately switch worker index back while proj1 is stopping
	sched.SetIndex(workerIndex, 2)
	wg.Wait()

	// Proj1 should be started again on workerIndex
	select {
	case <-projStarted:
		// Success! Pusher was restarted for proj1.
	case <-time.After(2 * time.Second):
		t.Fatal("Pusher was not restarted for proj1 after re-assignment")
	}

	require.NoError(t, sched.Stop())
}

func TestRemoveProject_WaitsForStoppingPusherAfterSetIndex(t *testing.T) {
	pushStarted := make(chan struct{}, 1)
	pushDone := make(chan struct{}, 1)

	sched := newPushersScheduler(0, 2, 2, 0, func(pc *ProjectConfig) Pusher {
		return &mockPusher{
			pushStarted: pushStarted,
			pushDone:    pushDone,
			blockPush:   true,
			cancelDelay: 60 * time.Millisecond,
		}
	})

	proj := &ProjectConfig{
		Server:  "example.com",
		Project: "proj1",
		URL:     "example.com/proj1",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}

	workerIndex := 0
	if !sched.cHasher.IsAssignedToCurrentWorker(proj.Name().String()) {
		workerIndex = 1
		sched.SetIndex(1, 2)
	}
	otherWorkerIndex := 1 - workerIndex

	require.NoError(t, sched.AddProject(proj, "test-repo"))
	<-pushStarted

	// Unassign project in background so it starts stopping and is removed from s.pushers
	go func() {
		sched.SetIndex(otherWorkerIndex, 2)
	}()
	time.Sleep(10 * time.Millisecond)

	// RemoveProject must wait until the stopping pusher has completely exited
	require.NoError(t, sched.RemoveProject(proj, "test-repo"))

	select {
	case <-pushDone:
	default:
		t.Fatal("RemoveProject returned before in-flight Push() completed")
	}

	require.NoError(t, sched.Stop())
}

func TestRemoveProject_MultiRepoRestartsWithRemainingConfig(t *testing.T) {
	pushed := make(chan *ProjectConfig, 10)

	sched := newPushersScheduler(0, 1, 1, 0, func(pc *ProjectConfig) Pusher {
		return &testPusher{config: pc, pushed: pushed}
	})

	projRepo1 := &ProjectConfig{
		Server:  "example.com",
		Project: "shared-proj",
		URL:     "example.com/shared-proj-repo1",
		FetchConfig: FetchConfig{
			PushInterval: 10 * time.Millisecond,
		},
	}
	projRepo2 := &ProjectConfig{
		Server:  "example.com",
		Project: "shared-proj",
		URL:     "example.com/shared-proj-repo2",
		FetchConfig: FetchConfig{
			PushInterval: 10 * time.Millisecond,
		},
	}

	require.NoError(t, sched.AddProject(projRepo1, "repo1"))
	require.NoError(t, sched.AddProject(projRepo2, "repo2"))

	require.Same(t, projRepo1, <-pushed)

	// Remove repo1; scheduler should switch active pusher to projRepo2
	require.NoError(t, sched.RemoveProject(projRepo1, "repo1"))

	for {
		if <-pushed == projRepo2 {
			break
		}
	}

	require.NoError(t, sched.Stop())
}

func TestRemoveProject_MultiRepoPassiveRemovalDoesNotRestartActivePusher(t *testing.T) {
	var pusherCreations int
	sched := newPushersScheduler(0, 1, 1, 0, func(pc *ProjectConfig) Pusher {
		pusherCreations++
		return &testPusher{config: pc}
	})

	projRepo1 := &ProjectConfig{
		Server:  "example.com",
		Project: "shared-proj",
		URL:     "example.com/shared-proj-repo1",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}
	projRepo2 := &ProjectConfig{
		Server:  "example.com",
		Project: "shared-proj",
		URL:     "example.com/shared-proj-repo2",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}
	projRepo3 := &ProjectConfig{
		Server:  "example.com",
		Project: "shared-proj",
		URL:     "example.com/shared-proj-repo3",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}

	require.NoError(t, sched.AddProject(projRepo1, "repo1"))
	require.NoError(t, sched.AddProject(projRepo2, "repo2"))
	require.NoError(t, sched.AddProject(projRepo3, "repo3"))
	require.Equal(t, 1, pusherCreations)

	// Removing passive repo2 must not cancel/restart active pusher for repo1
	require.NoError(t, sched.RemoveProject(projRepo2, "repo2"))
	require.Equal(t, 1, pusherCreations)
	require.Same(t, projRepo1, sched.projects["example.com/shared-proj"])

	require.NoError(t, sched.Stop())
}

func TestUpdateProject_MultiRepoPassiveUpdateDoesNotRestartActivePusher(t *testing.T) {
	var pusherCreations int
	sched := newPushersScheduler(0, 1, 1, 0, func(pc *ProjectConfig) Pusher {
		pusherCreations++
		return &testPusher{config: pc}
	})

	projRepo1 := &ProjectConfig{
		Server:  "example.com",
		Project: "shared-proj",
		URL:     "example.com/shared-proj-repo1",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}
	projRepo2 := &ProjectConfig{
		Server:  "example.com",
		Project: "shared-proj",
		URL:     "example.com/shared-proj-repo2",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}
	projRepo2Updated := &ProjectConfig{
		Server:  "example.com",
		Project: "shared-proj",
		URL:     "example.com/shared-proj-repo2-updated",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}

	require.NoError(t, sched.AddProject(projRepo1, "repo1"))
	require.NoError(t, sched.AddProject(projRepo2, "repo2"))
	require.Equal(t, 1, pusherCreations)

	// Updating passive repo2 must update repo2's config without restarting repo1's active pusher
	require.NoError(t, sched.UpdateProject(projRepo2Updated, "repo2"))
	require.Equal(t, 1, pusherCreations)
	require.Same(t, projRepo1, sched.projects["example.com/shared-proj"])

	// When repo1 is subsequently removed, the scheduler should switch to repo2's updated config
	require.NoError(t, sched.RemoveProject(projRepo1, "repo1"))
	require.Equal(t, 2, pusherCreations)
	require.Same(t, projRepo2Updated, sched.projects["example.com/shared-proj"])

	require.NoError(t, sched.Stop())
}

func TestRemoveProject_MultiRepoWaitsForStoppingPusherAfterSetIndex(t *testing.T) {
	pushStarted := make(chan struct{}, 1)
	pushDone := make(chan struct{}, 1)

	sched := newPushersScheduler(0, 2, 2, 0, func(pc *ProjectConfig) Pusher {
		return &mockPusher{
			pushStarted: pushStarted,
			pushDone:    pushDone,
			blockPush:   true,
			cancelDelay: 60 * time.Millisecond,
		}
	})

	projRepo1 := &ProjectConfig{
		Server:  "example.com",
		Project: "shared-proj",
		URL:     "example.com/shared-proj-repo1",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}
	projRepo2 := &ProjectConfig{
		Server:  "example.com",
		Project: "shared-proj",
		URL:     "example.com/shared-proj-repo2",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}

	workerIndex := 0
	if !sched.cHasher.IsAssignedToCurrentWorker(projRepo1.Name().String()) {
		workerIndex = 1
		sched.SetIndex(1, 2)
	}
	otherWorkerIndex := 1 - workerIndex

	require.NoError(t, sched.AddProject(projRepo1, "repo1"))
	require.NoError(t, sched.AddProject(projRepo2, "repo2"))
	<-pushStarted

	// Unassign project in background so it starts stopping and is removed from s.pushers
	go func() {
		sched.SetIndex(otherWorkerIndex, 2)
	}()
	time.Sleep(10 * time.Millisecond)

	// RemoveProject on repo1 (while repo2 still exists) must wait until the stopping pusher has completely exited
	require.NoError(t, sched.RemoveProject(projRepo1, "repo1"))

	select {
	case <-pushDone:
	default:
		t.Fatal("RemoveProject returned before in-flight Push() completed for multi-repo project")
	}

	require.NoError(t, sched.Stop())
}
