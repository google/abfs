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
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/google/abfs/rendezvous"
	"github.com/samber/go-psi"

	"github.com/google/abfs/shared/dagsync"
)

type Pusher interface {
	Push(ctx context.Context) error
	Stat() PusherStat
	SyncDump(ctx context.Context, w io.Writer)
	SyncValidate(ctx context.Context, w io.Writer, nodeStaleTimeout time.Duration)
}

type ProjectName string

func (p ProjectName) String() string {
	return string(p)
}

type PusherStat struct {
	Name         ProjectName
	Remote       string
	URL          string
	State        string
	UploadStart  string
	LastUpload   string
	SyncDuration float64
	LastError    string
	DagSync      *dagsync.Stats
}

type pushersScheduler struct {
	mu              sync.RWMutex
	totalPushers    int
	thisPusherIndex int
	maxMemoryUsage  float64
	repos           map[ProjectName]map[RepoName]*ProjectConfig
	projects        map[ProjectName]*ProjectConfig
	// pushers maps each active ProjectName on this worker to its running projPushWrap.
	pushers map[ProjectName]*projPushWrap
	// stopping tracks the stopped channel of the most recently started pusher for each project,
	// allowing newly started pushers to wait for their predecessor to exit before pushing.
	stopping  map[ProjectName]<-chan struct{}
	wg        sync.WaitGroup
	sem       chan struct{}
	cHasher   *rendezvous.Chasher
	newPusher func(*ProjectConfig) Pusher
}

func newPushersScheduler(thisPusherIndex, totalPushers, maxSyncs int, maxMemoryUsage float64, newPusher func(*ProjectConfig) Pusher) *pushersScheduler {
	return &pushersScheduler{
		thisPusherIndex: thisPusherIndex,
		totalPushers:    totalPushers,
		maxMemoryUsage:  maxMemoryUsage,
		repos:           make(map[ProjectName]map[RepoName]*ProjectConfig),
		projects:        make(map[ProjectName]*ProjectConfig),
		pushers:         make(map[ProjectName]*projPushWrap),
		stopping:        make(map[ProjectName]<-chan struct{}),
		sem:             make(chan struct{}, maxSyncs),
		cHasher:         rendezvous.NewChasher(totalPushers, thisPusherIndex),
		newPusher:       newPusher,
	}
}

func (s *pushersScheduler) SetMaxSyncs(maxSyncs int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cap(s.sem) == maxSyncs {
		return
	}

	s.sem = make(chan struct{}, maxSyncs)
}

func (s *pushersScheduler) SetMaxMemoryUsage(maxMemoryUsage float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxMemoryUsage = maxMemoryUsage
}

func (s *pushersScheduler) SetIndex(thisPusherIndex, totalPushers int) {
	s.mu.Lock()
	if s.thisPusherIndex == thisPusherIndex && s.totalPushers == totalPushers {
		s.mu.Unlock()
		return
	}

	s.cHasher = rendezvous.NewChasher(totalPushers, thisPusherIndex)
	s.thisPusherIndex = thisPusherIndex
	s.totalPushers = totalPushers

	var toWait []<-chan struct{}
	for name := range s.projects {
		if stopped := s.reconcileProject(name); stopped != nil {
			toWait = append(toWait, stopped)
		}
	}
	s.mu.Unlock()

	for _, stopped := range toWait {
		<-stopped
	}
}

func (s *pushersScheduler) waitIfMemoryHigh(ctx context.Context) {
	s.mu.RLock()
	maxMem := s.maxMemoryUsage
	s.mu.RUnlock()

	if maxMem == 0 {
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		stats, err := psi.PSIStatsForResource(psi.Memory)
		if err != nil {
			log.Printf("Unable to read memory PSI information")
			return
		}

		if stats.Some.Avg10 < maxMem {
			return
		}

		log.Printf("Memory usage too high (%f%% > %f%% ), throttling...", stats.Some.Avg10, maxMem)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// continue loop
		}
	}
}

func (s *pushersScheduler) AddProject(proj *ProjectConfig, repoName RepoName) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.addProject(proj, repoName)
}

func (s *pushersScheduler) addProject(proj *ProjectConfig, repoName RepoName) error {
	log.Printf("Adding project %q for repo %q", proj.Name(), repoName)

	r, found := s.repos[proj.Name()]
	if !found {
		r = make(map[RepoName]*ProjectConfig)
		s.repos[proj.Name()] = r
	}
	if _, found := r[repoName]; found {
		return fmt.Errorf("project %q already exists for repo %q", proj.Name(), repoName)
	}
	r[repoName] = proj
	if _, found := s.projects[proj.Name()]; !found {
		s.projects[proj.Name()] = proj
	}

	s.reconcileProject(proj.Name())
	return nil
}

func (s *pushersScheduler) startPusher(proj *ProjectConfig) {
	ctx, cancel := context.WithCancel(context.Background())
	log.Printf("Starting project %q", proj.Name())
	prevStopped := s.stopping[proj.Name()]
	p := &projPushWrap{
		config:      proj,
		cancel:      cancel,
		pusher:      s.newPusher(proj),
		stopped:     make(chan struct{}),
		prevStopped: prevStopped,
	}
	s.pushers[proj.Name()] = p
	s.stopping[proj.Name()] = p.stopped
	s.wg.Add(1)
	go s.runPusherLoop(ctx, p, prevStopped)
}

// stopPusher cancels the active pusher for name (if any), removes it from s.pushers,
// and returns the stopped channel for any stopping pusher for name. Must be called with s.mu held.
func (s *pushersScheduler) stopPusher(name ProjectName) <-chan struct{} {
	if pp, exists := s.pushers[name]; exists {
		log.Printf("Stopping project %q", pp)
		pp.cancel()
		delete(s.pushers, name)
	}
	return s.stopping[name]
}

// reconcileProject aligns the active pusher state in s.pushers[name] with the desired
// configuration for this worker (determined by s.projects[name] and s.cHasher).
//
//   - If the project is removed or unassigned from this worker, any active pusher is stopped.
//   - If the project is assigned to this worker, it ensures a pusher is running with the
//     desired ProjectConfig (stopping any mismatched pusher first).
//
// Returns a channel that closes when any previously active or replaced pusher for name has
// completely exited (or nil if none). Must be called with s.mu held; callers should release
// s.mu before waiting on the returned channel.
func (s *pushersScheduler) reconcileProject(name ProjectName) <-chan struct{} {
	var desired *ProjectConfig
	if cfg, exists := s.projects[name]; exists && s.cHasher.IsAssignedToCurrentWorker(name.String()) {
		desired = cfg
	}

	if desired == nil {
		return s.stopPusher(name)
	}

	if pp, exists := s.pushers[name]; exists {
		if pp.config == desired {
			return pp.prevStopped
		}
		s.stopPusher(name)
	}

	s.startPusher(desired)
	return s.pushers[name].prevStopped
}

func (s *pushersScheduler) RemoveProject(proj *ProjectConfig, repoName RepoName) error {
	s.mu.Lock()
	stopped, err := s.removeProject(proj, repoName)
	s.mu.Unlock()

	if err != nil {
		return err
	}
	if stopped != nil {
		<-stopped
	}
	return nil
}

func (s *pushersScheduler) removeProject(proj *ProjectConfig, repoName RepoName) (<-chan struct{}, error) {
	log.Printf("Removing project %q for repo %q", proj.Name(), repoName)

	repoProjs := s.repos[proj.Name()]
	removedConfig, found := repoProjs[repoName]
	if !found {
		return nil, fmt.Errorf("project %q not found for repo %q", proj.Name(), repoName)
	}
	delete(repoProjs, repoName)

	if len(repoProjs) > 0 {
		if s.projects[proj.Name()] == removedConfig {
			for _, rem := range repoProjs {
				s.projects[proj.Name()] = rem
				break
			}
		}
	} else {
		delete(s.repos, proj.Name())
		delete(s.projects, proj.Name())
	}

	return s.reconcileProject(proj.Name()), nil
}

func (s *pushersScheduler) UpdateProject(proj *ProjectConfig, repoName RepoName) error {
	s.mu.Lock()
	if _, found := s.projects[proj.Name()]; !found {
		s.mu.Unlock()
		return fmt.Errorf("project %q not found", proj.Name())
	}

	repoProjs, found := s.repos[proj.Name()]
	if !found || repoProjs[repoName] == nil {
		s.mu.Unlock()
		return fmt.Errorf("project %q not found for repo %q", proj.Name(), repoName)
	}

	oldConfig := repoProjs[repoName]
	repoProjs[repoName] = proj
	if s.projects[proj.Name()] == oldConfig {
		s.projects[proj.Name()] = proj
		s.stopPusher(proj.Name())
	}

	stopped := s.reconcileProject(proj.Name())
	s.mu.Unlock()

	if stopped != nil {
		<-stopped
	}
	return nil
}

func (s *pushersScheduler) runPusherLoop(ctx context.Context, pp *projPushWrap, prevStopped <-chan struct{}) {
	log.Printf("Starting pusher loop for project %s", pp)
	defer log.Printf("Stopped pusher loop for project %s", pp)

	defer s.wg.Done()
	defer pp.cancel()
	defer func() {
		if prevStopped != nil {
			<-prevStopped
		}
		s.mu.Lock()
		if s.stopping[pp.config.Name()] == pp.stopped {
			delete(s.stopping, pp.config.Name())
		}
		s.mu.Unlock()
		close(pp.stopped)
	}()

	if prevStopped != nil {
		select {
		case <-ctx.Done():
			return
		case <-prevStopped:
		}
	}

	for {
		s.waitIfMemoryHigh(ctx)

		// Keep a local copy of the semaphore to avoid releasing the token to a different semaphore.
		s.mu.RLock()
		sem := s.sem
		s.mu.RUnlock()

		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}

		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Pusher loop for project %s panicked: %v", pp, r)
				}
			}()

			defer func() {
				<-sem
			}()

			err := pp.push(ctx)
			if errors.Is(err, context.Canceled) {
				return
			} else if err != nil {
				log.Printf("Project push %s failed: %v", pp, err)
			} else {
				log.Printf("Project push %s succeeded", pp)
			}
		}()

		select {
		case <-ctx.Done():
			return
		case <-time.After(pp.config.PushInterval):
		}
	}
}

func (s *pushersScheduler) ContainsProject(proj ProjectName) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, found := s.projects[proj]
	return found
}

func (s *pushersScheduler) Projects() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := make([]string, 0, len(s.projects))
	for p := range s.projects {
		stats = append(stats, p.String())
	}
	return stats
}

func (s *pushersScheduler) ProjectsStats() []PusherStat {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := make([]PusherStat, 0, len(s.projects))
	for _, s := range s.pushers {
		stats = append(stats, s.pusher.Stat())
	}
	return stats
}

func (s *pushersScheduler) cancelAll() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, pp := range s.pushers {
		pp.cancel()
	}
}

func (s *pushersScheduler) Stop() error {
	s.cancelAll()
	s.wg.Wait()
	return nil
}

var ErrProjectNotFound = errors.New("project not found")

func (s *pushersScheduler) SyncDump(ctx context.Context, projName ProjectName, w io.Writer) error {
	s.mu.RLock()
	pp, found := s.pushers[projName]
	if !found {
		log.Printf("projName %q, map %+v", projName, s.pushers)
		s.mu.RUnlock()
		return ErrProjectNotFound
	}
	s.mu.RUnlock()
	pp.pusher.SyncDump(ctx, w)
	return nil
}

func (s *pushersScheduler) SyncValidate(ctx context.Context, projName ProjectName, w io.Writer, nodeStaleTimeout time.Duration) error {
	s.mu.RLock()
	pp, found := s.pushers[projName]
	s.mu.RUnlock()
	if !found {
		return ErrProjectNotFound
	}
	pp.pusher.SyncValidate(ctx, w, nodeStaleTimeout)
	return nil
}

type projPushWrap struct {
	config *ProjectConfig
	cancel context.CancelFunc
	pusher Pusher
	// stopped is closed when this pusher instance's runPusherLoop goroutine has completely exited
	// (and after its prevStopped channel has also closed).
	stopped chan struct{}
	// prevStopped is the stopped channel of the immediately preceding pusher instance for the
	// same project (if any). runPusherLoop waits on prevStopped before starting its first push
	// (and before closing stopped on exit) to serialize consecutive pushers for the same project.
	prevStopped <-chan struct{}
}

func (p *projPushWrap) push(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	log.Printf("Pushing project %s", p.config.URL)
	return p.pusher.Push(ctx)
}

func (p *projPushWrap) String() string {
	return p.config.Name().String()
}
