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
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/git"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
)

const (
	packedRefsFile = "packed-refs"
)

type Hash = entities.Hash

type CASGit struct {
	store    entities.CASStore
	shellgit *git.ShellGit
	casfs    bindcas.CASFS
	client   *binding.Client
	cacheman *client.Client
}

func New(casStore entities.CASStore) *CASGit {
	return &CASGit{
		store:    casStore,
		shellgit: git.NewShellGit(""),
	}
}

func NewWithCASFS(casStore entities.CASStore, casfs bindcas.CASFS) *CASGit {
	return &CASGit{
		store:    casStore,
		shellgit: git.NewShellGit(""),
		casfs:    casfs,
	}
}

func NewWithClient(client *binding.Client, cacheman *client.Client) *CASGit {
	return &CASGit{
		store:    client.Store(),
		shellgit: git.NewShellGit(""),
		casfs:    bindcas.NewExternalCASFS(client.Root()),
		client:   client,
		cacheman: cacheman,
	}
}

func (g *CASGit) GetSymRef(ctx context.Context, server string, project string, ref string) (string, error) {
	refs, err := g.store.GetRefs(server, project, []string{ref})
	if entities.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if len(refs) == 0 || refs[0].IsZero() {
		return "", nil
	}

	obj, err := binding.FetchObject[gitobjects.SymRefObject](ctx, g.store, refs[0])
	if err != nil {
		return "", err
	}
	return obj.Content, nil
}

func (g *CASGit) ResolveRef(ctx context.Context, server, project, ref string) (entities.Hash, string, error) {
	lookupOrder := []string{"refs/heads/" + ref, "refs/tags/" + ref}

	hashes, err := g.store.GetRefs(server, project, lookupOrder)
	if entities.IsNotExist(err) {
		return entities.Hash{}, "", nil
	} else if err != nil {
		return entities.Hash{}, "", err
	}

	for i, h := range hashes {
		if !h.IsZero() {
			return h, lookupOrder[i], nil
		}
	}

	return entities.Hash{}, "", nil
}

func (g CASGit) GetCommit(ctx context.Context, key entities.Hash) (*object.Commit, error) {
	commitBytes, err := g.store.GetObject(ctx, key)
	if err != nil {
		return nil, err
	}
	t, buf, err := entities.DecodeObject(commitBytes)
	if err != nil {
		return nil, err
	}
	if t != plumbing.CommitObject.String() {
		return nil, fmt.Errorf("object %s is not a commit (is a %s)", key, t)
	}

	c := &object.Commit{}
	if err := c.Decode(newCASObject(plumbing.Hash(key), plumbing.CommitObject, buf)); err != nil {
		return nil, err
	}
	return c, nil
}

// ResolveCommit will return the Commit object for a hash referencing a commit or a tag
func (g CASGit) ResolveCommit(ctx context.Context, key entities.Hash) (*object.Commit, error) {
	for {
		commitBytes, err := g.store.GetObject(ctx, key)
		if err != nil {
			return nil, err
		}
		t, buf, err := entities.DecodeObject(commitBytes)
		if err != nil {
			return nil, err
		}

		switch t {
		case plumbing.CommitObject.String():
			c := &object.Commit{}
			if err := c.Decode(newCASObject(plumbing.Hash(key), plumbing.CommitObject, buf)); err != nil {
				return nil, err
			}
			return c, nil
		case plumbing.TagObject.String():
			tag := &object.Tag{}
			if err := tag.Decode(newCASObject(plumbing.Hash(key), plumbing.TagObject, buf)); err != nil {
				return nil, err
			}

			key = entities.Hash(tag.Target)
			continue
		default:
			return nil, fmt.Errorf("object %s is not a commit or tag (is a %s)", key, t)
		}
	}
}

func FindGitDir(startDir string) (string, error) {
	dir := startDir
	for {
		gitDir := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			return gitDir, nil
		} else if !entities.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", entities.NotExistErrf("not a git repository (or any of the parent directories): .git")
		}
		dir = parent
	}
}

const (
	commitMsgHookServer  = "gerrit.googlesource.com"
	commitMsgHookProject = "tools/hooks/"
	commitMsgHookRef     = "refs/gerrit-commit-hooks/stable-abfs"
)

func SetupCentralCommitMsgHook(ctx context.Context, store entities.CASStore, casfs bindcas.CASFS, repoRoot string) {
	centralFile := filepath.Join(repoRoot, ".repo", "repo", "hooks", "commit-msg")

	// Check if the hook already exists to avoid setting up again
	if _, err := os.Stat(centralFile); err == nil {
		return
	}

	setupHook := func() error {
		refs, err := store.SearchRefs(commitMsgHookServer, commitMsgHookProject, commitMsgHookRef, 1)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			return fmt.Errorf("no hook ref found for server %s, project %s, and ref %s", commitMsgHookServer, commitMsgHookProject, commitMsgHookRef)
		}
		hash := refs[0].Hash

		header, err := store.ObjectHeader(ctx, hash)
		if err != nil {
			return err
		}
		size := header.Size

		centralDir := filepath.Dir(centralFile)
		if err := os.MkdirAll(centralDir, 0755); err != nil {
			return fmt.Errorf("failed to create central hooks directory: %w", err)
		}

		// Exclude the central hooks directory from CASFS
		if err := casfs.SetExcluded(centralDir, true); err != nil {
			return fmt.Errorf("failed to exclude central hooks directory: %w", err)
		}

		if err := casfs.Mknod(centralFile, entities.FileModeExecutable, 0); err != nil {
			return fmt.Errorf("failed to create central hook node: %w", err)
		}

		if err := casfs.SetHashSize(centralFile, hash, size); err != nil {
			return fmt.Errorf("failed to set central hook hash: %w", err)
		}

		return nil
	}

	if err := setupHook(); err != nil {
		log.Printf("Warning: Failed to centrally resolve/install Gerrit commit-msg hook: %v", err)
		log.Printf("The central hook setup will be skipped or has failed. You will need to manually install the Gerrit commit-msg hook for Change-Id generation in each project.")
		log.Printf("To install manually, run inside each synced project's git directory:")
		log.Printf("  curl -Lo .git/hooks/commit-msg https://gerrit-review.googlesource.com/tools/hooks/commit-msg && chmod +x .git/hooks/commit-msg")
		log.Printf("If the error was due to missing ref, you can push the hook hash using:")
		log.Printf("  echo \"<hash> %s\" | abfs test-client put-ref --project %q --server %q", commitMsgHookRef, commitMsgHookProject, commitMsgHookServer)
	}
}
