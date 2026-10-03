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

package casgit

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/filesystem/dirsync"
	"github.com/google/abfs/shared/entities"
)

const (
	gitIndicesPrefetchPriority = 0.2
	prefetchSymlinksPriority   = 0.3
)

type GitRepo struct {
	casgit *CASGit

	remoteURL  string
	server     string
	project    string
	workDir    string
	gitDir     string
	objectsDir string

	config   *GitConfig
	head     string
	headHash entities.Hash
	remotes  []*Remote
	branches []*Branch
	refs     map[string]string
}

type Remote struct {
	Name    string
	URL     string
	PushURL string
	Fetch   []RefSpec
	Mirror  bool
	TagOpts string
}

type Branch struct {
	Name        string
	Remote      string
	Merge       string
	Rebase      bool
	PushRemote  string
	Description string
}

func (b *Branch) TrackingRef() string {
	// TODO: We need to read the mapping from refspecs
	if b.Remote == "" || b.Merge == "" {
		return ""
	}
	shortName := strings.TrimPrefix(b.Merge, "refs/heads/")
	return fmt.Sprintf("remotes/%s/%s", b.Remote, shortName)
}

func NewGitRepo(casgit *CASGit, workDir, gitDir, objectsDir string) *GitRepo {
	return &GitRepo{
		casgit:     casgit,
		workDir:    workDir,
		gitDir:     gitDir,
		objectsDir: objectsDir,
	}
}

func (r *GitRepo) Server() string {
	return r.server
}

func (r *GitRepo) Project() string {
	return r.project
}

func (r *GitRepo) setRemoteURL(remoteURL string) error {
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return fmt.Errorf("failed to parse repository URL %s: %v", remoteURL, err)
	}
	switch parsed.Scheme {
	case "":
		return fmt.Errorf("repository URL must be a remote repository")
	}
	r.remoteURL = remoteURL
	r.server = parsed.Host
	r.project = strings.Trim(parsed.Path, "/")
	return nil
}

func (r *GitRepo) addPacks(ctx context.Context) error {
	packs, err := binding.FetchPacksFromStore(ctx, r.server, r.project, r.casgit.client.Store())
	if err != nil {
		return fmt.Errorf("failed to fetch packs from store for server: %q project: %q err: %w", r.server, r.project, err)
	}
	if err := addGitPacks(packs, r.casgit.casfs, r.objectsDir); err != nil {
		return err
	}

	indices := make([]entities.Hash, 0, len(packs.Packs))
	for _, pack := range packs.Packs {
		indices = append(indices, pack.Index)
	}
	if err := r.casgit.cacheman.Prefetch(indices, nil, gitIndicesPrefetchPriority); err != nil {
		log.Printf("cannot prefetch packs for project %s: %v", r.remoteURL, err)
	}
	return nil
}

func (r *GitRepo) findRef(ref string) string {
	if _, found := r.refs[ref]; found {
		return ref
	}
	if _, found := r.refs["refs/heads/"+ref]; found {
		return "refs/heads/" + ref
	}
	if _, found := r.refs["refs/tags/"+ref]; found {
		return "refs/tags/" + ref
	}
	return ""
}

func (r *GitRepo) resolveRef(ref string) (Hash, error) {
	data, ok := r.refs[ref]
	if !ok {
		return Hash{}, entities.NotExistErrf("cannot find ref %s", ref)
	}

	return r.resolveRefContent(data)
}

func (r *GitRepo) resolveRefContent(refContent string) (Hash, error) {
	if entities.IsHash(refContent) {
		return entities.NewHash(refContent), nil
	}
	if ref, ok := strings.CutPrefix(refContent, "ref: "); ok {
		return r.resolveRef(ref)
	}
	return Hash{}, fmt.Errorf("invalid ref content: %s", refContent)
}

func (r *GitRepo) Load() error {
	if err := r.LoadConfig(); err != nil {
		return err
	}
	if err := r.loadRemotes(); err != nil {
		return err
	}
	if err := r.loadBranches(); err != nil {
		return err
	}
	if err := r.loadRefs(); err != nil {
		return err
	}
	if err := r.loadCurrentHead(); err != nil {
		return err
	}
	return nil
}

func (r *GitRepo) LoadConfig() (retErr error) {
	f, err := os.Open(path.Join(r.gitDir, "config"))
	if err != nil {
		return fmt.Errorf("failed to open git config file: %w", err)
	}
	defer entities.CloseErrCheck(f, &retErr)

	config, err := ParseGitConfig(f)
	if err != nil {
		return fmt.Errorf("failed to parse git config: %w", err)
	}
	r.config = config
	return nil
}

func (r *GitRepo) loadRefs() error {
	refs, err := r.casgit.readRefs(r.gitDir, nil)
	if err != nil {
		return err
	}
	r.refs = refs
	return nil
}

func (r *GitRepo) loadCurrentHead() error {
	data, err := os.ReadFile(path.Join(r.gitDir, "HEAD"))
	if err != nil {
		if entities.IsNotExist(err) {
			return entities.NotExistErrf("cannot find the HEAD file in git directory: %s", r.gitDir)
		}
		return err
	}
	r.head = strings.TrimSpace(string(data))

	h, err := r.resolveRefContent(r.head)
	if err != nil {
		return err
	}
	r.headHash = h
	return nil
}

func (r *GitRepo) IsDetached() bool {
	return !strings.HasPrefix(r.head, "ref: ")
}

func (r *GitRepo) CurrentBranch() (*Branch, error) {
	if r.IsDetached() {
		return nil, entities.NotExistErrf("HEAD is detached")
	}
	ref := strings.TrimPrefix(r.head, "ref: ")
	branchName := strings.TrimPrefix(ref, "refs/heads/")
	for _, branch := range r.branches {
		if branch.Name == branchName {
			return branch, nil
		}
	}
	return nil, entities.NotExistErrf("cannot find the current branch for ref %s", ref)
}

func (r *GitRepo) GetBranch(name string) *Branch {
	for _, branch := range r.branches {
		if branch.Name == name {
			return branch
		}
	}
	return nil
}

func (r *GitRepo) GetRemote(remote string) (*Remote, error) {
	for _, r := range r.remotes {
		if r.Name == remote {
			return r, nil
		}
	}
	return nil, fmt.Errorf("remote %q not found", remote)
}

func (r *GitRepo) loadRemotes() error {
	configs := r.config.Group("remote")
	remotes := make([]*Remote, 0, len(configs))

	for _, group := range configs {
		remote := &Remote{Name: group.Name}
		for i, key := range group.Keys {
			value := group.Values[i]
			switch key {
			case "url":
				remote.URL = value
			case "pushurl":
				remote.PushURL = value
			case "fetch":
				spec, err := ParseRefSpec(value)
				if err != nil {
					return fmt.Errorf("invalid fetch refspec %q for remote %q: %w", value, group.Name, err)
				}
				remote.Fetch = append(remote.Fetch, spec)
			case "mirror":
				remote.Mirror = value == "true"
			case "tagopt":
				remote.TagOpts = value
			}
		}
		remotes = append(remotes, remote)
	}
	r.remotes = remotes
	return nil
}

func (r *GitRepo) loadBranches() error {
	configs := r.config.Group("branch")
	branches := make([]*Branch, 0, len(configs))

	for _, group := range configs {
		branch := &Branch{Name: group.Name}
		for i, key := range group.Keys {
			value := group.Values[i]
			switch key {
			case "remote":
				branch.Remote = value
			case "merge":
				branch.Merge = value
			case "rebase":
				branch.Rebase = value == "true"
			case "pushRemote":
				branch.PushRemote = value
			case "description":
				branch.Description = value
			}
		}
		branches = append(branches, branch)
	}
	r.branches = branches
	return nil
}

func (r *GitRepo) GetAllConfig(key string) ([]string, error) {
	return r.config.GetAll(key)
}

func (r *GitRepo) GetConfig(key, defaultValue string) (string, error) {
	val, found, err := r.config.Get(key)
	if err != nil {
		return "", err
	}
	if !found {
		return defaultValue, nil
	}
	return val, nil
}

func (r *GitRepo) AddConfig(key, value string) error {
	if err := r.config.Add(key, value); err != nil {
		return err
	}
	return WriteGitConfig(r.gitDir, r.config)
}

func (r *GitRepo) SetConfig(key, value string) error {
	if err := r.config.Set(key, value); err != nil {
		return err
	}
	return WriteGitConfig(r.gitDir, r.config)
}

func (r *GitRepo) UnsetConfig(key string) error {
	if err := r.config.Unset(key); err != nil {
		return err
	}
	return WriteGitConfig(r.gitDir, r.config)
}

func (r *GitRepo) RevParse(ref string) (string, error) {
	return r.casgit.shellgit.RevParse(r.workDir, ref)
}

func (r *GitRepo) DiffHeadWorkDir(ctx context.Context) ([]dirsync.DiffAction, error) {
	curTree, err := r.casgit.casfs.Hash(r.workDir)
	if err != nil {
		return nil, err
	}
	return r.DiffHeadTree(ctx, curTree)
}

func (r *GitRepo) DiffHeadTree(ctx context.Context, treeHash entities.Hash) ([]dirsync.DiffAction, error) {
	headC, err := r.casgit.ResolveCommit(ctx, r.headHash)
	if err != nil {
		return nil, err
	}
	return dirsync.DirDiffList(ctx, r.casgit.store, entities.Hash(headC.TreeHash), treeHash, true)
}

func (r *GitRepo) LocalCommits(branchName string) ([]string, error) {
	if branchName == "" {
		return nil, nil
	}
	if branch := r.GetBranch(branchName); branch != nil {
		trackingRef := branch.TrackingRef()
		if trackingRef != "" {
			return r.casgit.shellgit.RevListReverse(r.workDir, trackingRef, branchName)
		}
	}
	return nil, nil
}

func (r *GitRepo) HasUncommitedChanges() (bool, error) {
	return r.casgit.shellgit.HasUncommitedChanges(r.workDir)
}

func (r *GitRepo) RebaseCommitsOnto(projectName string, localCommits []string, revision string) error {
	if len(localCommits) == 0 {
		return nil
	}

	oldbase := localCommits[0] + "^"
	tip := localCommits[len(localCommits)-1]
	if err := r.casgit.shellgit.RebaseOnto(r.workDir, revision, oldbase, tip); err != nil {
		return fmt.Errorf("failed to rebase local commits for project %s: %w", projectName, err)
	}

	return nil
}

func (r *GitRepo) UpdateLocalBranchToHead(projectName string, branchName string) error {
	headHash, err := r.RevParse("HEAD")
	if err != nil {
		return fmt.Errorf("failed to get HEAD hash for project %s: %w", projectName, err)
	}
	if err := r.casgit.shellgit.UpdateRef(r.workDir, "refs/heads/"+branchName, headHash); err != nil {
		return fmt.Errorf("failed to update branch ref for project %s: %w", projectName, err)
	}
	if err := r.casgit.shellgit.SetSymbolicRef(r.workDir, "HEAD", "refs/heads/"+branchName); err != nil {
		return fmt.Errorf("failed to symbolic checkout branch for project %s: %w", projectName, err)
	}
	return nil
}
