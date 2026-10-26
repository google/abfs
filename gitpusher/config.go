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
	"fmt"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/google/abfs/binding"
)

const (
	defaultMaxRemotePushes        = 100
	defaultMaxProjectSyncs        = 10
	defaultDAGSyncWorkers         = 20
	defaultProjectPushInterval    = 60 * time.Second
	defaultProtocol               = "https"
	defaultManifestProject        = "platform/manifest"
	defaultManifestBranch         = "main"
	defaultManifestFile           = "manifest.xml"
	defaultManifestsCheckInterval = 60 * time.Second
	defaultPackPushInterval       = 30 * time.Minute
)

var (
	defaultRefSpecs = []string{"refs/*:refs/*"}
)

type GitPusherPoolConfig struct {
	Name         string             `yaml:"name"`
	PushersCount int                `yaml:"pushers-count"`
	Pushers      []*GitPusherConfig `yaml:"pushers"`
	Repos        []*RepoConfig      `yaml:"repos"`
	Projects     []*ProjectConfig   `yaml:"projects"`
	Defaults     struct {
		NamePattern      string  `yaml:"name-pattern"`
		Port             int     `yaml:"port"`
		MaxRemotePushes  int     `yaml:"max-remote-pushes"`
		MaxProjectsSyncs int     `yaml:"max-projects-syncs"`
		MaxMemoryUsage   float64 `yaml:"max-memory-usage"`
	} `yaml:"defaults"`
}
type GitPusherConfig struct {
	Name             string  `yaml:"name"`
	Index            *int    `yaml:"index"`
	Inactive         bool    `yaml:"inactive"`
	URL              string  `yaml:"url"`
	Port             int     `yaml:"port"`
	MaxRemotePushes  int     `yaml:"max-remote-pushes"`
	MaxProjectsSyncs int     `yaml:"max-projects-syncs"`
	MaxMemoryUsage   float64 `yaml:"max-memory-usage"`
}
type ManifestFile struct {
	// Branch is a branch name, or a pattern with one "*" as in a git refspec.
	Branch string `yaml:"branch"`
	File   string `yaml:"file"`
}

func (f ManifestFile) fetchRefSpec(remote string) config.RefSpec {
	return config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", f.Branch, remote, f.Branch))
}

func (f ManifestFile) isPattern() bool {
	return strings.Contains(f.Branch, "*")
}

// MatchesBranch reports whether branch is the branch of f, or matches it as a pattern.
func (f ManifestFile) MatchesBranch(branch string) bool {
	return f.fetchRefSpec(defaultRemoteName).Match(plumbing.ReferenceName("refs/heads/" + branch))
}

type ManifestConfig struct {
	URL           string         `yaml:"url"`
	Server        string         `yaml:"server"`
	Project       string         `yaml:"project"`
	Files         []ManifestFile `yaml:"files"`
	CheckInterval time.Duration  `yaml:"check-interval"`
}
type RepoConfig struct {
	Manifest        ManifestConfig `yaml:"manifest"`
	ProjectDefaults FetchConfig    `yaml:"project-defaults"`
	Overrides       []struct {
		Name         string         `yaml:"name"`
		Regex        bool           `yaml:"regex"`
		RegexPattern *regexp.Regexp `yaml:"-"`
		Exclude      bool           `yaml:"exclude"`
		FetchConfig  `yaml:",inline"`
	} `yaml:"overrides"`
}
type ProjectConfig struct {
	Server      string `yaml:"server"`
	Project     string `yaml:"project"`
	URL         string `yaml:"url"`
	FetchConfig `yaml:",inline"`
}

type FetchConfig struct {
	FetchRefSpecs    []string                `yaml:"fetch-ref-spec"`
	PushInterval     time.Duration           `yaml:"push-interval"`
	LFS              bool                    `yaml:"lfs"`
	Workers          int                     `yaml:"workers"`
	PackPushInterval time.Duration           `yaml:"pack-push-interval"`
	PackingStrategy  binding.PackingStrategy `yaml:"packing-strategy"`
}

func (f *FetchConfig) Override(g *FetchConfig) {
	if g.FetchRefSpecs != nil {
		f.FetchRefSpecs = g.FetchRefSpecs
	}
	if g.PushInterval != 0 {
		f.PushInterval = g.PushInterval
	}
	if g.LFS {
		f.LFS = true
	}
	if g.Workers != 0 {
		f.Workers = g.Workers
	}
	if g.PackPushInterval != 0 {
		f.PackPushInterval = g.PackPushInterval
	}
	if g.PackingStrategy != "" {
		f.PackingStrategy = g.PackingStrategy
	}
}

// ValidateRefSpecs checks the fetch ref specs as git would read them.
func (f *FetchConfig) ValidateRefSpecs() error {
	for _, spec := range f.FetchRefSpecs {
		if strings.HasPrefix(spec, "^") {
			if strings.Contains(spec, ":") {
				return fmt.Errorf("negative-ref-spec %q must not contain a : character", spec)
			}
		} else {
			newSpec := config.RefSpec(spec)
			if err := newSpec.Validate(); err != nil {
				return fmt.Errorf("fetch-ref-spec %q is invalid: %w", spec, err)
			}
		}
	}
	return nil
}

// StoresRef reports whether these ref specs map a remote ref to the local name ref, the name the pusher
// pushes it under. Tags that git fetches because they point into fetched history are not covered.
func (f *FetchConfig) StoresRef(ref string) bool {
	specs := f.FetchRefSpecs
	if len(specs) == 0 {
		specs = defaultRefSpecs
	}
	var negative []config.RefSpec
	for _, spec := range specs {
		if pattern, ok := strings.CutPrefix(spec, "^"); ok {
			negative = append(negative, config.RefSpec(pattern+":"+pattern))
		}
	}
	for _, spec := range specs {
		if strings.HasPrefix(spec, "^") || !strings.Contains(spec, ":") || config.RefSpec(spec).Validate() != nil {
			continue
		}
		spec = strings.TrimPrefix(spec, "+")
		reversed := config.RefSpec(spec).Reverse()
		if !reversed.Match(plumbing.ReferenceName(ref)) {
			continue
		}
		src := reversed.Dst(plumbing.ReferenceName(ref))
		if !slices.ContainsFunc(negative, func(n config.RefSpec) bool { return n.Match(src) }) {
			return true
		}
	}
	return false
}

// ProjectFetchConfig returns the fetch config of a manifest project and whether an override excludes it.
func (c *RepoConfig) ProjectFetchConfig(name string) (FetchConfig, bool) {
	fetchConfig := c.ProjectDefaults
	excluded := false
	for _, override := range c.Overrides {
		matches := false
		if override.Regex {
			if override.RegexPattern.MatchString(name) {
				matches = true
			}
		} else if override.Name == name {
			matches = true
		}
		if matches {
			if override.Exclude {
				excluded = true
			}
			fetchConfig.Override(&override.FetchConfig)
		}
	}
	return fetchConfig, excluded
}

func (c *GitPusherPoolConfig) Instance(name string) (*GitPusherConfig, error) {
	for _, pusher := range c.Pushers {
		if pusher.Name == name {
			return pusher, nil
		}
	}
	return nil, fmt.Errorf("no pusher found for %s", name)
}

// Validate checks if the configuration is valid.
// It also populates some internal fields and default values.
func (c *GitPusherPoolConfig) Validate() error {
	if c.PushersCount < 0 {
		return fmt.Errorf("invalid pushers-count %d", c.PushersCount)
	}

	activePushers := make(map[string]struct{}, len(c.Pushers))
	activeIndexes := make(map[int]struct{}, len(c.Pushers))
	for index, pusher := range c.Pushers {
		if pusher.Index == nil {
			pusher.Index = &index
		}

		if pusher.Port == 0 {
			pusher.Port = c.Defaults.Port
		}
		if pusher.Name == "" {
			pusher.Name = strings.ReplaceAll(c.Defaults.NamePattern, "{index}", strconv.Itoa(*pusher.Index))
		}

		if pusher.MaxRemotePushes == 0 {
			pusher.MaxRemotePushes = c.Defaults.MaxRemotePushes
		}
		if pusher.MaxRemotePushes == 0 {
			pusher.MaxRemotePushes = defaultMaxRemotePushes
		}

		if pusher.MaxProjectsSyncs == 0 {
			pusher.MaxProjectsSyncs = c.Defaults.MaxProjectsSyncs
		}
		if pusher.MaxProjectsSyncs == 0 {
			pusher.MaxProjectsSyncs = defaultMaxProjectSyncs
		}

		if pusher.MaxMemoryUsage == 0 {
			pusher.MaxMemoryUsage = c.Defaults.MaxMemoryUsage
		}

		if pusher.MaxMemoryUsage < 0 || pusher.MaxMemoryUsage > 100 {
			return fmt.Errorf("invalid max-memory-usage %f for pusher %s, must be between 0 and 100", pusher.MaxMemoryUsage, pusher.Name)
		}

		if !pusher.Inactive {
			if _, found := activePushers[pusher.Name]; found {
				return fmt.Errorf("duplicate pusher name %s", pusher.Name)
			}
			activePushers[pusher.Name] = struct{}{}

			if _, found := activeIndexes[*pusher.Index]; found {
				return fmt.Errorf("duplicate pusher index %d", *pusher.Index)
			}
			activeIndexes[*pusher.Index] = struct{}{}
			if *pusher.Index < 0 || *pusher.Index >= c.PushersCount {
				return fmt.Errorf("invalid pusher index %d for %s", *pusher.Index, pusher.Name)
			}
		}

	}

	repoNames := make(map[RepoName]struct{}, len(c.Repos))
	for _, repo := range c.Repos {
		if err := repo.Validate(); err != nil {
			return fmt.Errorf("invalid repo %s: %w", repo.Name(), err)
		}
		if _, found := repoNames[repo.Name()]; found {
			return fmt.Errorf("duplicate repo %s", repo.Name())
		}
		repoNames[repo.Name()] = struct{}{}
	}

	projectNames := make(map[ProjectName]struct{}, len(c.Projects))
	for _, proj := range c.Projects {
		if err := proj.Validate(); err != nil {
			return fmt.Errorf("invalid project %s: %w", proj.URL, err)
		}

		if _, found := projectNames[proj.Name()]; found {
			return fmt.Errorf("duplicate project %s", proj.Name())
		}
		projectNames[proj.Name()] = struct{}{}
	}

	return nil
}

func (c *RepoConfig) Validate() error {
	if err := validateGitServerAndProject(&c.Manifest.URL, &c.Manifest.Server, &c.Manifest.Project); err != nil {
		return err
	}

	if c.Manifest.CheckInterval == 0 {
		c.Manifest.CheckInterval = defaultManifestsCheckInterval
	}

	if c.Manifest.Project == "" {
		c.Manifest.Project = defaultManifestProject
	}

	if len(c.Manifest.Files) == 0 {
		c.Manifest.Files = []ManifestFile{{File: defaultManifestFile, Branch: defaultManifestBranch}}
	}

	for i := range c.Manifest.Files {
		f := &c.Manifest.Files[i]
		if f.Branch == "" {
			f.Branch = defaultManifestBranch
		}
		if f.File == "" {
			f.File = defaultManifestFile
		}
		if err := f.fetchRefSpec(defaultRemoteName).Validate(); err != nil {
			return fmt.Errorf("invalid manifest branch %q: %w", f.Branch, err)
		}
	}

	for i, override := range c.Overrides {
		if override.Name == "" {
			return fmt.Errorf("invalid override %d: missing name", i)
		}
		if override.Regex {
			regex, err := regexp.Compile(override.Name)
			if err != nil {
				return fmt.Errorf("invalid regex pattern %q: %w", override.Name, err)
			}
			c.Overrides[i].RegexPattern = regex
		}
	}
	return nil
}

// ListsManifestFile reports whether the git-pusher reads file on branch to find the projects of this repo.
func (c *RepoConfig) ListsManifestFile(branch, file string) bool {
	return slices.ContainsFunc(c.Manifest.Files, func(f ManifestFile) bool {
		return f.File == file && f.MatchesBranch(branch)
	})
}

func (c *RepoConfig) Name() RepoName {
	return RepoName(c.Manifest.Server + "/" + c.Manifest.Project)
}

func cleanPath(p string) string {
	return strings.Trim(path.Clean(p), "/")
}

func validateGitServerAndProject(gitURLPtr, serverPtr, projectPtr *string) error {
	gitURL, server, project := *gitURLPtr, *serverPtr, *projectPtr

	if gitURL == "" {
		if server == "" || project == "" {
			return fmt.Errorf("invalid project with no url, server or path")
		}

		gitURL = (&url.URL{
			Scheme: defaultProtocol,
			Host:   cleanPath(server),
			Path:   cleanPath(project),
		}).String()
	}

	url, err := parseURLWithDefaultScheme(gitURL, defaultProtocol)
	if err != nil {
		return fmt.Errorf("failed to parse URL %s: %w", gitURL, err)
	}
	if url.Host == "" {
		return fmt.Errorf("invalid URL %s: missing host", gitURL)
	}
	if url.Path == "" || url.Path == "/" {
		return fmt.Errorf("invalid URL %s: missing path", gitURL)
	}
	if gitURL != url.String() {
		gitURL = url.String()
	}

	if server == "" {
		server = url.Host
	}
	if project == "" {
		project = strings.Trim(url.Path, "/")
	}

	server = cleanPath(server)
	project = cleanPath(project)

	*gitURLPtr = gitURL
	*serverPtr = server
	*projectPtr = project
	return nil
}

func (c *ProjectConfig) Validate() error {
	if err := validateGitServerAndProject(&c.URL, &c.Server, &c.Project); err != nil {
		return err
	}

	if len(c.FetchRefSpecs) == 0 {
		c.FetchRefSpecs = defaultRefSpecs
	}

	if err := c.ValidateRefSpecs(); err != nil {
		return err
	}

	if c.PackingStrategy == "" {
		c.PackingStrategy = binding.PackingStrategyTrees
	}
	if err := c.PackingStrategy.Validate(); err != nil {
		return err
	}

	if c.Workers == 0 {
		c.Workers = defaultDAGSyncWorkers
	}
	if c.PushInterval == 0 {
		c.PushInterval = defaultProjectPushInterval
	}
	if c.PackPushInterval == 0 {
		c.PackPushInterval = defaultPackPushInterval
	}

	return nil
}

func (c *ProjectConfig) Name() ProjectName {
	return ProjectName(c.Server + "/" + c.Project)
}

func parseURLWithDefaultScheme(rawURL, defaultScheme string) (*url.URL, error) {
	if strings.HasPrefix(rawURL, "//") {
		rawURL = defaultScheme + ":" + rawURL
	} else if !strings.Contains(rawURL, "://") {
		rawURL = defaultScheme + "://" + rawURL
	}

	return url.Parse(rawURL)
}

func diffByKey[T any](oldItems, newItems []T, getKey func(T) string) (add, remove, update []T) {
	if len(oldItems) == 0 {
		return newItems, nil, nil
	}

	oldsMap := make(map[string]T, len(oldItems))
	for _, proj := range oldItems {
		oldsMap[getKey(proj)] = proj
	}

	for _, item := range newItems {
		if oldProj, found := oldsMap[getKey(item)]; found {
			delete(oldsMap, getKey(item))
			if !reflect.DeepEqual(oldProj, item) {
				update = append(update, item)
			}
		} else {
			add = append(add, item)
		}
	}
	for _, item := range oldsMap {
		remove = append(remove, item)
	}

	return
}
