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
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type mockPusher struct {
	mu          sync.Mutex
	pushStarted chan struct{}
	pushDone    chan struct{}
	blockPush   bool
	cancelDelay time.Duration
	onPushEnter func()
	onPushExit  func()
}

func (m *mockPusher) Push(ctx context.Context) error {
	if m.onPushEnter != nil {
		m.onPushEnter()
	}
	if m.onPushExit != nil {
		defer m.onPushExit()
	}
	if m.pushStarted != nil {
		select {
		case m.pushStarted <- struct{}{}:
		default:
		}
	}
	if m.blockPush {
		<-ctx.Done()
		if m.cancelDelay > 0 {
			time.Sleep(m.cancelDelay)
		}
	}
	if m.pushDone != nil {
		select {
		case m.pushDone <- struct{}{}:
		default:
		}
	}
	return ctx.Err()
}

func (m *mockPusher) Stat() PusherStat                                                     { return PusherStat{} }
func (m *mockPusher) SyncDump(ctx context.Context, w io.Writer)                            {}
func (m *mockPusher) SyncValidate(ctx context.Context, w io.Writer, timeout time.Duration) {}

// TestDeadlock_EndToEndSequentialRemoveProject tests the exact sequence from the logs:
//  1. ProjA is actively pushing and takes time to cancel/exit (holding s.mu.Lock() inside RemoveProject(ProjA)).
//  2. ProjB's PushInterval expires while s.mu.Lock() is held, blocking ProjB on s.mu.RLock().
//  3. ProjA finishes and unlocks s.mu, but RemoveProject(ProjB) is called immediately in sequence.
//  4. Without the fix, RemoveProject(ProjB) deadlocks because it holds s.mu.Lock() while waiting
//     for ProjB's pusher loop to stop, which is blocked trying to acquire s.mu.RLock().
func TestDeadlock_EndToEndSequentialRemoveProject(t *testing.T) {
	proj1Started := make(chan struct{}, 1)
	proj2Pushed := make(chan struct{}, 1)

	pusher1 := &mockPusher{
		pushStarted: proj1Started,
		blockPush:   true,
		cancelDelay: 80 * time.Millisecond,
	}

	pusher2 := &mockPusher{
		pushDone: proj2Pushed,
	}

	sched := newPushersScheduler(0, 1, 2, 0, func(pc *ProjectConfig) Pusher {
		if pc.Project == "proj1" {
			return pusher1
		}
		return pusher2
	})

	proj1Config := &ProjectConfig{
		Server:  "example.com",
		Project: "proj1",
		URL:     "example.com/proj1",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}
	proj2Config := &ProjectConfig{
		Server:  "example.com",
		Project: "proj2",
		URL:     "example.com/proj2",
		FetchConfig: FetchConfig{
			PushInterval: 10 * time.Millisecond,
		},
	}

	require.NoError(t, sched.AddProject(proj1Config, "test-repo"))
	require.NoError(t, sched.AddProject(proj2Config, "test-repo"))

	// Wait for proj1 to be actively pushing
	<-proj1Started
	// Wait for proj2 to complete initial push and enter sleep
	<-proj2Pushed

	// Sequentially remove proj1 and proj2, just like applyProjectChanges does
	removalDone := make(chan struct{})
	go func() {
		defer close(removalDone)
		_ = sched.RemoveProject(proj1Config, "test-repo")
		_ = sched.RemoveProject(proj2Config, "test-repo")
	}()

	select {
	case <-removalDone:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("DEADLOCK: RemoveProject(proj2) hung and did not complete within 1s")
	}
}

func TestReAddProject_WaitsForOldPusherToStop(t *testing.T) {
	proj1Started := make(chan struct{}, 1)
	proj1Done := make(chan struct{}, 1)
	proj2Started := make(chan struct{}, 1)

	var (
		mu              sync.Mutex
		pusherCallCount int
	)

	sched := newPushersScheduler(0, 1, 2, 0, func(pc *ProjectConfig) Pusher {
		mu.Lock()
		pusherCallCount++
		count := pusherCallCount
		mu.Unlock()

		if count == 1 {
			return &mockPusher{
				pushStarted: proj1Started,
				pushDone:    proj1Done,
				blockPush:   true,
				cancelDelay: 80 * time.Millisecond,
			}
		}
		return &mockPusher{
			pushStarted: proj2Started,
		}
	})

	projConfig := &ProjectConfig{
		Server:  "example.com",
		Project: "proj1",
		URL:     "example.com/proj1",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}

	require.NoError(t, sched.AddProject(projConfig, "test-repo"))
	<-proj1Started

	// In the background, initiate RemoveProject
	go func() {
		_ = sched.RemoveProject(projConfig, "test-repo")
	}()

	// Small sleep to ensure RemoveProject has acquired lock and initiated teardown
	time.Sleep(10 * time.Millisecond)

	// Concurrently call AddProject for the same project
	addDone := make(chan struct{})
	go func() {
		defer close(addDone)
		require.NoError(t, sched.AddProject(projConfig, "test-repo"))
	}()

	select {
	case <-proj2Started:
		// Verify that proj1 has already completed its Push when proj2 starts
		select {
		case <-proj1Done:
			// Success: old pusher was done before new pusher started!
		default:
			t.Fatal("Race detected: new pusher started while old pusher was still in Push()")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for new pusher to start")
	}

	<-addDone
	require.NoError(t, sched.Stop())
}

func TestMultipleConcurrentReAdds_NoConcurrentPushers(t *testing.T) {
	var (
		activePushes    atomic.Int32
		maxActivePushes atomic.Int32
		pushStarted     = make(chan struct{}, 10)
	)

	sched := newPushersScheduler(0, 1, 4, 0, func(pc *ProjectConfig) Pusher {
		return &mockPusher{
			pushStarted: pushStarted,
			blockPush:   true,
			cancelDelay: 40 * time.Millisecond,
			onPushEnter: func() {
				cur := activePushes.Add(1)
				for {
					prev := maxActivePushes.Load()
					if cur <= prev || maxActivePushes.CompareAndSwap(prev, cur) {
						break
					}
				}
			},
			onPushExit: func() {
				activePushes.Add(-1)
			},
		}
	})

	projConfig := &ProjectConfig{
		Server:  "example.com",
		Project: "proj1",
		URL:     "example.com/proj1",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}

	require.NoError(t, sched.AddProject(projConfig, "repo1"))
	<-pushStarted

	// Initiate RemoveProject in background while proj1 is actively pushing
	go func() {
		_ = sched.RemoveProject(projConfig, "repo1")
	}()
	time.Sleep(5 * time.Millisecond)

	// Concurrently hammer AddProject, RemoveProject, and UpdateProject across multiple goroutines
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = sched.AddProject(projConfig, "repo1")
			_ = sched.UpdateProject(projConfig, "repo1")
			_ = sched.RemoveProject(projConfig, "repo1")
			_ = sched.AddProject(projConfig, "repo1")
		}(i)
	}
	wg.Wait()

	// Wait for the final pusher to start pushing
	select {
	case <-pushStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for final pusher to start")
	}

	require.Equal(t, int32(1), maxActivePushes.Load(), "At most 1 pusher should ever be inside Push() concurrently for the same project")
	require.NoError(t, sched.Stop())
}

func TestSyncDumpAndStatsDuringTeardown(t *testing.T) {
	pushStarted := make(chan struct{}, 2)

	sched := newPushersScheduler(0, 1, 2, 0, func(pc *ProjectConfig) Pusher {
		return &mockPusher{
			pushStarted: pushStarted,
			blockPush:   true,
			cancelDelay: 60 * time.Millisecond,
		}
	})

	projConfig := &ProjectConfig{
		Server:  "example.com",
		Project: "proj1",
		URL:     "example.com/proj1",
		FetchConfig: FetchConfig{
			PushInterval: 1 * time.Hour,
		},
	}

	require.NoError(t, sched.AddProject(projConfig, "repo1"))
	<-pushStarted

	// Start UpdateProject in background (will block for 60ms waiting for old pusher)
	updateDone := make(chan struct{})
	go func() {
		defer close(updateDone)
		require.NoError(t, sched.UpdateProject(projConfig, "repo1"))
	}()
	time.Sleep(10 * time.Millisecond)

	// While UpdateProject is in-flight, SyncDump and SyncValidate must NOT return ErrProjectNotFound
	var buf bytes.Buffer
	require.NoError(t, sched.SyncDump(context.Background(), projConfig.Name(), &buf))
	require.NoError(t, sched.SyncValidate(context.Background(), projConfig.Name(), &buf, time.Second))
	require.Len(t, sched.ProjectsStats(), 1)

	<-updateDone

	// Now initiate RemoveProject in background (will block for 60ms waiting for pusher)
	removeDone := make(chan struct{})
	go func() {
		defer close(removeDone)
		require.NoError(t, sched.RemoveProject(projConfig, "repo1"))
	}()
	time.Sleep(10 * time.Millisecond)

	// While RemoveProject is in-flight, ProjectsStats must immediately exclude the removed project
	require.Empty(t, sched.ProjectsStats())
	require.ErrorIs(t, sched.SyncDump(context.Background(), projConfig.Name(), &buf), ErrProjectNotFound)

	<-removeDone
	require.NoError(t, sched.Stop())
}
