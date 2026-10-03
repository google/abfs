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
	"log"
	"sync"
	"time"
)

type ProjectLister interface {
	ListProjects(ctx context.Context) ([]*ProjectConfig, error)
	Stat() RepoStat
}

type RepoName string

func (r RepoName) String() string {
	return string(r)
}

type RepoStat struct {
	Name          RepoName
	Remote        string
	URL           string
	State         string
	FetchStart    string
	FetchDuration float64
	LastSuccess   string
	LastError     string
}

type repoScheduler struct {
	mu               sync.RWMutex
	repos            map[RepoName]*repoWrap
	wg               sync.WaitGroup
	newLister        func(*RepoConfig) ProjectLister
	projectScheduler *pushersScheduler
}

func (s *repoScheduler) ReposStats() []RepoStat {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := make([]RepoStat, 0, len(s.repos))
	for _, r := range s.repos {
		stats = append(stats, r.lister.Stat())
	}
	return stats
}

func newRepoScheduler(newLister func(*RepoConfig) ProjectLister, projectScheduler *pushersScheduler) *repoScheduler {
	return &repoScheduler{
		repos:            make(map[RepoName]*repoWrap),
		newLister:        newLister,
		projectScheduler: projectScheduler,
	}
}

func (s *repoScheduler) AddRepo(config *RepoConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	log.Printf("Adding repo %q", config.Name())

	if _, found := s.repos[config.Name()]; found {
		return fmt.Errorf("repo %q already exists", config.Name())
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := &repoWrap{
		config: config,
		cancel: cancel,
		lister: s.newLister(config),
	}
	s.repos[config.Name()] = r
	r.stopped = make(chan struct{})
	s.wg.Add(1)
	go s.runPusherLoop(ctx, cancel, r)
	return nil
}

func (s *repoScheduler) UpdateRepo(config *RepoConfig) error {
	if err := s.RemoveRepo(config); err != nil {
		log.Printf("Error removing repo %q: %v", config.Name(), err)
	}
	return s.AddRepo(config)
}

func (s *repoScheduler) RemoveRepo(config *RepoConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	log.Printf("Removing repo %q", config.Name())

	wrapper, found := s.repos[config.Name()]
	if !found {
		return fmt.Errorf("repo %q not found", config.Name())
	}

	wrapper.cancel()
	<-wrapper.stopped

	for _, proj := range wrapper.projects {
		if err := s.projectScheduler.RemoveProject(proj, config.Name()); err != nil {
			log.Printf("Failed to remove project %q from repo %q: %v", proj.Name(), config.Name(), err)
		}
	}
	delete(s.repos, config.Name())

	return nil
}

func (s *repoScheduler) runPusherLoop(ctx context.Context, cancel context.CancelFunc, pp *repoWrap) {
	log.Printf("Starting pusher loop for manifest %s", pp)
	defer log.Printf("Stopped pusher loop for manifest %s", pp)

	defer s.wg.Done()
	defer cancel()
	defer close(pp.stopped)

	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("Projects listing for repo %s panicked: %v", pp, r)
				}
			}()

			projects, err := pp.listProjects(ctx)
			if errors.Is(err, context.Canceled) {
				return
			}
			if err != nil {
				log.Printf("listing projects for repo %s failed: %v", pp, err)
				return
			}

			s.applyProjectChanges(pp.config.Name(), pp.projects, projects)
			pp.projects = projects
		}()

		select {
		case <-ctx.Done():
			return
		case <-time.After(pp.config.Manifest.CheckInterval):
		}
	}
}

func (s *repoScheduler) applyProjectChanges(repoName RepoName, prevProjects []*ProjectConfig, projects []*ProjectConfig) {
	add, remove, update := diffByKey(prevProjects, projects, func(p *ProjectConfig) string {
		return p.Name().String()
	})
	for _, proj := range add {
		if err := s.projectScheduler.AddProject(proj, repoName); err != nil {
			log.Printf("Failed to add project %q to repo %q: %v", proj.Name(), repoName, err)
		}
	}
	for _, proj := range remove {
		if err := s.projectScheduler.RemoveProject(proj, repoName); err != nil {
			log.Printf("Failed to remove project %q from repo %q: %v", proj.Name(), repoName, err)
		}
	}
	for _, proj := range update {
		if err := s.projectScheduler.UpdateProject(proj, repoName); err != nil {
			log.Printf("Failed to update project %q in repo %q: %v", proj.Name(), repoName, err)
		}
	}
}

type repoWrap struct {
	config   *RepoConfig
	cancel   context.CancelFunc
	lister   ProjectLister
	projects []*ProjectConfig
	stopped  chan struct{}
}

func (p *repoWrap) listProjects(ctx context.Context) ([]*ProjectConfig, error) {
	log.Printf("Listing projects for repo %s", p.config.Name())
	return p.lister.ListProjects(ctx)
}

func (p *repoWrap) String() string {
	return p.config.Name().String()
}
