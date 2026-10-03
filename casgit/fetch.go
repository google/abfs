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
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
)

type FetchOptions struct {
	Remote    string
	RefSpecs  []string
	All       bool
	Prune     bool
	NoTags    bool
	Tags      bool
	GitDir    string
	ObjectDir string
	Quiet     bool
	Verbose   bool
}

type GitFetch struct {
	FetchOptions
	*GitRepo

	remoteURL string
	refSpecs  []RefSpec
}

func (g *CASGit) Fetch(ctx context.Context, opts FetchOptions) error {
	gitRepo := NewGitRepo(g, opts.GitDir, opts.GitDir, opts.ObjectDir)
	if err := gitRepo.Load(); err != nil {
		return fmt.Errorf("failed to load git repo: %w", err)
	}
	f := &GitFetch{
		FetchOptions: opts,
		GitRepo:      gitRepo,
	}

	return f.Fetch(ctx)
}

func (f *GitFetch) parseRefSpecs() error {
	refSpecStrs := f.RefSpecs
	if len(refSpecStrs) == 0 {
		specs, err := f.GetAllConfig(fmt.Sprintf("remote.%s.fetch", f.Remote))
		if err != nil {
			return fmt.Errorf("failed to get config: %w", err)
		}
		if len(specs) != 0 {
			refSpecStrs = specs
		} else {
			refSpecStrs = []string{fmt.Sprintf("+refs/heads/*:refs/remotes/%s/*", f.Remote)}
		}
	}

	specs, err := ParseRefSpecs(refSpecStrs)
	if err != nil {
		return fmt.Errorf("failed to parse refspecs: %w", err)
	}
	f.refSpecs = specs
	return nil
}

func (f *GitFetch) Fetch(ctx context.Context) error {
	if err := f.setRemoteInfo(); err != nil {
		return err
	}

	if err := f.parseRefSpecs(); err != nil {
		return err
	}

	if f.Verbose {
		fmt.Printf("Fetching from %s/%s...\n", f.server, f.project)
	}

	if err := f.updateRefs(); err != nil {
		return err
	}

	if err := f.addPacks(ctx); err != nil {
		return err
	}

	return nil
}

func (g *GitFetch) updateRefs() error {
	refUpdates, remotes, err := g.diffRemoteRefs()
	if err != nil {
		return err
	}

	if !g.Quiet {
		for _, refUpdate := range refUpdates {
			fmt.Printf(" %s\n", refUpdate.LongFormat())
		}
	}

	var grp errgroup.Group
	var lock sync.Mutex
	grp.SetLimit(runtime.NumCPU())

	for _, u := range refUpdates {
		grp.Go(func() error {
			if err := g.casgit.updateRef(g.GitDir, u.DestRef, u.NewHash); err != nil {
				return fmt.Errorf("failed to write ref %q: %w", u.DestRef, err)
			}
			lock.Lock()
			g.refs[u.DestRef] = u.NewHash.String()
			lock.Unlock()
			return nil
		})
	}
	if err := grp.Wait(); err != nil {
		return err
	}

	return g.writeFetchHead(remotes)
}

type RefUpdate struct {
	SourceRef string
	DestRef   string
	OldHash   entities.Hash
	NewHash   entities.Hash
}

func (u *RefUpdate) LongFormat() string {
	var from, to string
	switch {
	case strings.HasPrefix(u.SourceRef, "refs/tags/"):
		from = strings.TrimPrefix(u.SourceRef, "refs/tags/")
		to = strings.TrimPrefix(u.DestRef, "refs/tags/")

	case strings.HasPrefix(u.SourceRef, "refs/heads/"):
		from = strings.TrimPrefix(u.SourceRef, "refs/heads/")
		to = strings.TrimPrefix(u.DestRef, "refs/remotes/")

	default:
		from = u.SourceRef
		to = u.DestRef
	}

	return fmt.Sprintf(" %s %s -> %s", u.status(), from, to)
}

func (u *RefUpdate) status() string {
	if !u.OldHash.IsZero() {
		return fmt.Sprintf("%s..%s", u.OldHash, u.NewHash)
	}

	switch {
	case strings.HasPrefix(u.SourceRef, "refs/tags/"):
		return "[new tag]"
	case strings.HasPrefix(u.SourceRef, "refs/heads/"):
		return "[new branch]"
	default:
		return "[new ref]"
	}
}

func (g *GitFetch) diffRemoteRefs() ([]RefUpdate, map[string]entities.Hash, error) {
	prefix := RefSpecsPrefix(g.refSpecs)
	remoteRefs, err := g.casgit.client.Store().SearchRefs(g.server, g.project, prefix, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to search refs: %w", err)
	}

	// TODO: limit to specs
	refs, err := g.casgit.readRefs(g.GitDir, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read refs: %w", err)
	}

	updates := make([]RefUpdate, 0)
	remotes := make(map[string]entities.Hash)
	for _, ref := range remoteRefs {
		if destRef, ok := RefSpecsMatch(ref.Ref, g.refSpecs); ok {
			remotes[ref.Ref] = ref.Hash
			if refs[destRef] == ref.Hash.String() {
				continue
			}
			updates = append(updates, RefUpdate{
				SourceRef: ref.Ref,
				DestRef:   destRef,
				OldHash:   entities.NewHash(refs[destRef]),
				NewHash:   ref.Hash,
			})
		}
	}
	return updates, remotes, nil
}

func (g *GitFetch) writeFetchHead(remoteRefs map[string]entities.Hash) (retErr error) {
	fetchHeadPath := path.Join(g.GitDir, "FETCH_HEAD")
	f, err := os.OpenFile(fetchHeadPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open FETCH_HEAD for writing: %w", err)
	}
	defer entities.CloseErrCheck(f, &retErr)

	buf := bufio.NewWriter(f)
	for ref, hash := range remoteRefs {
		refName := strings.TrimPrefix(ref, "refs/heads/")
		desc := fmt.Sprintf("branch '%s' of %s", refName, g.remoteURL)
		if _, err := fmt.Fprintf(buf, "%s\t\t%s\n", hash, desc); err != nil {
			return err
		}
	}
	return buf.Flush()
}

func (f *GitFetch) setRemoteInfo() error {
	if f.Remote == "" {
		if !f.IsDetached() {
			branch, err := f.CurrentBranch()
			if err != nil {
				log.Printf("failed to get the current branch from %q: %v", f.GitDir, err)
			}
			f.Remote = branch.Remote
		}
		if f.Remote == "" && len(f.remotes) > 0 {
			f.Remote = f.remotes[0].Name
		}
		if f.Remote == "" {
			f.Remote = "origin"
		}
	}
	remote, err := f.GetRemote(f.Remote)
	if err != nil {
		return err
	}

	return f.setRemoteURL(remote.URL)
}
