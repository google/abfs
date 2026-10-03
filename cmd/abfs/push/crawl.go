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

// TODO: Should submodules be supported? (submodule trees have 'commit' entries)
package push

import (
	"context"
	"fmt"

	"github.com/google/abfs/shared/entities"
	abfsclient "github.com/google/abfs/shared/remote/grpc/client"

	shellgit "github.com/google/abfs/git"

	"github.com/gammazero/deque"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	pobject "github.com/go-git/go-git/v5/plumbing/object"
)

// excludes queries for the presence of hashes in a depth-by-depth fashion of
// the ancestry tree from HEAD commit. Searches stop at cache hits so that
// the return exclude list is minimal.
func excludes(repo *git.Repository, client *abfsclient.ABFSClient) ([]entities.Hash, error) {
	hash, err := repo.ResolveRevision(plumbing.Revision("HEAD"))
	if err != nil {
		return nil, err
	}

	var excludes []entities.Hash
	queue := new(deque.Deque[entities.Hash])
	queue.PushBack(entities.Hash(*hash))
	seen := map[entities.Hash]bool{
		entities.Hash(*hash): true,
	}
	for queue.Len() > 0 {
		req := []entities.Hash{}

		// Issue requests for everything at this depth in the tree.
		for queue.Len() > 0 {
			front := queue.PopFront()
			req = append(req, front)
		}

		hdrs, err := client.Headers(context.Background(), req)
		if err != nil {
			return nil, err
		}

		for i, hdr := range hdrs {
			if !hdr.IsEmpty() {
				excludes = append(excludes, req[i])
			} else {
				commit, err := pobject.GetCommit(repo.Storer, plumbing.Hash(req[i]))
				if err != nil {
					return nil, err
				}
				for _, p := range commit.ParentHashes {
					entitiesHash := entities.Hash(p)
					if ok := seen[entitiesHash]; ok {
						continue
					}

					queue.PushBack(entitiesHash)
					seen[entitiesHash] = true
				}
			}
		}
	}

	return excludes, nil
}

type Runner interface {
	// ObjCount returns the number of objects reachable from rev but unreachable
	// from the exclude set.
	ObjCount(dir, rev string, excludes []entities.Hash) (int64, error)

	// RevListObjectsTopoOrder outputs objects reachable from rev but unreachable
	// from the exclude set. Commits are in topological order.
	RevListObjectsTopoOrder(dir, rev string, excludes []entities.Hash) (<-chan shellgit.ObjectTypeNameStreamItem, error)
}

type runner struct {
}

func (r *runner) ObjCount(dir, rev string, excludes []entities.Hash) (int64, error) {
	shellGit := &shellgit.ShellGit{BinPath: "git"}

	return shellGit.GetObjCount(dir, rev, excludes)
}

func (r *runner) RevListObjectsTopoOrder(dir, rev string, excludes []entities.Hash) (<-chan shellgit.ObjectTypeNameStreamItem, error) {
	shellGit := &shellgit.ShellGit{BinPath: "git"}

	return shellGit.RevListObjectsTopoOrder(dir, rev, excludes)
}

var defaultRunner = &runner{}

// Crawler will crawl a commit-ish revision tree and stream out objects in an
// order such that dependee blobs and trees appear before dependent commits.
// It makes the following assumptions:
//   - Storage consumers are free of dangling commits. This affords us the
//     ability to prune commits from the rev-list search without missing
//     uncached objects.
type Crawler struct {
	RepoPath        string
	ParentRev       entities.Hash
	ExcludedObjects []entities.Hash
	Runner
}

func NewCrawler(repoPath string, parentRev entities.Hash, excludedObjects []entities.Hash) *Crawler {
	return &Crawler{
		RepoPath:        repoPath,
		ParentRev:       parentRev,
		ExcludedObjects: excludedObjects,
		Runner:          defaultRunner,
	}
}

func (c *Crawler) count() (int64, error) {
	return c.ObjCount(c.RepoPath, c.ParentRev.String(), c.ExcludedObjects)
}

func (c *Crawler) validateConfig() error {
	gitRepo, err := git.PlainOpen(c.RepoPath)
	if err != nil {
		return err
	}

	if _, err := gitRepo.Object(plumbing.AnyObject, plumbing.Hash(c.ParentRev)); err != nil {
		return err
	}

	return nil
}

func (c *Crawler) objects() (<-chan shellgit.ObjectTypeNameStreamItem, error) {
	unorderedObjectStream, err := c.RevListObjectsTopoOrder(c.RepoPath, c.ParentRev.String(), c.ExcludedObjects)
	if err != nil {
		return nil, err
	}

	orderedObjectsStream := make(chan shellgit.ObjectTypeNameStreamItem)
	go func() {
		defer func() {
			close(orderedObjectsStream)
		}()

		commits := []shellgit.ObjectTypeNameStreamItem{}
		changesObjects := []shellgit.ObjectTypeNameStreamItem{}

		for obj := range unorderedObjectStream {
			switch obj.ObjType {
			case plumbing.CommitObject:
				// Hold onto commits until all objects are sent.
				for i := len(changesObjects) - 1; i >= 0; i-- {
					orderedObjectsStream <- changesObjects[i]
				}
				changesObjects = []shellgit.ObjectTypeNameStreamItem{}
				commits = append(commits, obj)
			case plumbing.TreeObject, plumbing.BlobObject:
				// Transmit blob and tree objects in reverse order.
				changesObjects = append(changesObjects, obj)
			default:
				orderedObjectsStream <- shellgit.ObjectTypeNameStreamItem{Err: fmt.Errorf("unknown type %v", obj.ObjType)}
				return
			}

		}
		for i := len(changesObjects) - 1; i >= 0; i-- {
			orderedObjectsStream <- changesObjects[i]
		}
		for _, c := range commits {
			orderedObjectsStream <- c
		}
	}()

	return orderedObjectsStream, nil
}

// Objects outputs object names in a safe topological order where all
// dependee objects occur before their dependent commits.
func (c *Crawler) Objects() (<-chan shellgit.ObjectTypeNameStreamItem, error) {
	if err := c.validateConfig(); err != nil {
		return nil, err
	}

	return c.objects()
}
