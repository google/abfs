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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testLister returns new configs for the same projects on every listing, as RepoWatcher does.
type testLister struct {
	mu           sync.Mutex
	pushInterval time.Duration
	err          error
}

func (l *testLister) set(pushInterval time.Duration, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pushInterval, l.err = pushInterval, err
}

func (l *testLister) ListProjects(ctx context.Context) ([]*ProjectConfig, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	var projects []*ProjectConfig
	for _, name := range []string{"a", "b"} {
		p := &ProjectConfig{URL: "https://example.com/" + name, FetchConfig: FetchConfig{PushInterval: l.pushInterval}}
		if err := p.Validate(); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, nil
}

func (l *testLister) Stat() RepoStat {
	return RepoStat{}
}

func TestUpdateRepo_KeepsProjectsRunning(t *testing.T) {
	var mu sync.Mutex
	var created int
	pushers := newPushersScheduler(0, 1, 10, 0, func(pc *ProjectConfig) Pusher {
		mu.Lock()
		defer mu.Unlock()
		created++
		return &testPusher{config: pc}
	})
	lister := &testLister{pushInterval: time.Hour}
	repos := newRepoScheduler(func(*RepoConfig) ProjectLister { return lister }, pushers)
	pushersCreated := func() int {
		mu.Lock()
		defer mu.Unlock()
		return created
	}
	// Waits for several listings of the current loop.
	settle := func() { time.Sleep(100 * time.Millisecond) }

	config := func(file string) *RepoConfig {
		return &RepoConfig{Manifest: ManifestConfig{
			Server:        "example.com",
			Project:       "platform/manifest",
			Files:         []ManifestFile{{Branch: "main", File: file}},
			CheckInterval: 10 * time.Millisecond,
		}}
	}

	require.NoError(t, repos.AddRepo(config("default.xml")))
	require.Eventually(t, func() bool { return pushersCreated() == 2 }, 5*time.Second, 10*time.Millisecond)

	// A manifest files edit restarts no pusher.
	require.NoError(t, repos.UpdateRepo(config("other.xml")))
	settle()
	require.Equal(t, 2, pushersCreated())

	// A failing listing after an edit keeps the projects.
	lister.set(time.Hour, errors.New("fetch failed"))
	require.NoError(t, repos.UpdateRepo(config("third.xml")))
	settle()
	require.ElementsMatch(t, []string{"example.com/a", "example.com/b"}, pushers.Projects())
	require.Equal(t, 2, pushersCreated())

	// A changed fetch config restarts each project once.
	lister.set(2*time.Hour, nil)
	require.NoError(t, repos.UpdateRepo(config("default.xml")))
	require.Eventually(t, func() bool { return pushersCreated() == 4 }, 5*time.Second, 10*time.Millisecond)
	settle()
	require.Equal(t, 4, pushersCreated())

	require.NoError(t, repos.RemoveRepo(config("default.xml")))
	require.NoError(t, pushers.Stop())
}
