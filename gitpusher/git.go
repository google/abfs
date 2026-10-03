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
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/google/abfs/fastgit"
	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/shared/dagsync"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

const (
	defaultRemoteName = "origin"
)

type dagSyncObjectAccessor struct {
	repo *fastgit.Repository
}

// GetObject must return an entities.NotExistErr if the object is a blob. Dagsync
// depends on this contract.
func (a *dagSyncObjectAccessor) GetObject(ctx context.Context, h entities.Hash) (content []byte, retErr error) {
	r, err := a.repo.OpenObject(h)
	if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(r, &retErr)
	t, size, err := objects.ReadHeader(r)
	if err != nil {
		return nil, err
	}
	if t == string(entities.ObjectTypeBlob) {
		return nil, entities.NotExistErrf("object %s is a blob, expected a non-blob type", h)
	}
	return io.ReadAll(objects.PrependHeaderReader(r, t, size))
}

func (a *dagSyncObjectAccessor) OpenBlob(ctx context.Context, h entities.Hash) (dagsync.ReadSizeCloser, error) {
	r, err := a.repo.OpenObject(h)
	if err != nil {
		return nil, err
	}
	size, err := objects.ReadBlobHeader(r)
	if err != nil {
		if err := r.Close(); err != nil {
			log.Printf("Failed to close reader: %s", err)
		}
		return nil, fmt.Errorf("error while reading the blob header: %w", err)
	}
	return &blobReadSizeCloser{r, size}, nil
}

type blobReadSizeCloser struct {
	rc   io.ReadCloser
	size int64
}

func (b blobReadSizeCloser) Close() error {
	return b.rc.Close()
}

func (b blobReadSizeCloser) Read(p []byte) (int, error) {
	return b.rc.Read(p)
}

func (b blobReadSizeCloser) Size() (int64, error) {
	return b.size, nil
}

// ensureGitRepo initializes a bare git repository at projPath if it doesn't already exist.
// Unlike getOrCreateGitRepo, it does not open the repository with go-git, avoiding config
// parse errors when .git/config contains negative refspecs (which go-git v5.19+ does not support).
func ensureGitRepo(projPath string) error {
	fs := osfs.New(projPath)
	s := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	_, err := git.Init(s, nil)
	if err == nil || err == git.ErrRepositoryAlreadyExists {
		return nil
	}
	return fmt.Errorf("failed to initialize git repo at path %s: %w", projPath, err)
}

func getOrCreateGitRepo(projPath string) (*git.Repository, error) {
	fs := osfs.New(projPath)
	// NewStorage creates the directory at projPath if it doesn't exist
	s := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	r, err := git.Init(s, nil) // nil worktree gives us a bare repository
	if err == git.ErrRepositoryAlreadyExists {
		r, err = git.PlainOpen(projPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open existing repository at path %s: %w", projPath, err)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to initialize git repo at path %s: %w", projPath, err)
	}
	return r, nil
}

// fetchWithRetry attempts to fetch a git repository and retries upon specific errors.
func fetchWithRetry(shgit *shellgit.ShellGit, repoPath string) error {
	const maxRetries = 3
	var err error

	badObjRefsCount := 0
	emptyObjFileCount := 0

	for i := 0; i < maxRetries; i++ {
		err = shgit.Fetch(repoPath)
		if err == nil {
			return nil // Success
		}

		errStr := err.Error()

		if strings.Contains(errStr, "fatal: bad object refs/") {
			// Handle the first type of error message: "fatal: bad object refs/"
			badObjRefsCount++
			if badObjRefsCount >= 2 {
				log.Printf("git fetch error matched 'fatal: bad object refs/' twice in %s. Giving up.", repoPath)
				return err
			}

			log.Printf("git fetch error matched 'fatal: bad object refs/' in %s: %v. Cleaning up refs and retrying %d/%d...", repoPath, err, i+1, maxRetries)

			refsPath := filepath.Join(repoPath, "refs")

			_ = os.RemoveAll(refsPath)

			if err := os.MkdirAll(refsPath, 0755); err != nil {
				return fmt.Errorf("failed to create refs directory %s: %w", refsPath, err)
			}

			cmdGc := exec.Command(shgit.BinPath, "gc", "--prune=now")
			cmdGc.Dir = repoPath
			if err := cmdGc.Run(); err != nil {
				return fmt.Errorf("failed to run git gc in %s: %w", repoPath, err)
			}

			cmdPack := exec.Command(shgit.BinPath, "pack-refs", "--all", "--prune")
			cmdPack.Dir = repoPath
			if err := cmdPack.Run(); err != nil {
				return fmt.Errorf("failed to run git pack-refs in %s: %w", repoPath, err)
			}
		} else if strings.Contains(errStr, "error: object file") && strings.Contains(errStr, "is empty") {
			// Handle the second type of error message: "error: object file ... is empty"
			emptyObjFileCount++
			if emptyObjFileCount >= 2 {
				log.Printf("git fetch error matched 'error: object file ... is empty' twice in %s. Giving up.", repoPath)
				return err
			}

			log.Printf("git fetch error matched 'error: object file ... is empty' in %s: %v. Cleaning up empty objects and retrying %d/%d...", repoPath, err, i+1, maxRetries)

			objectsPath := filepath.Join(repoPath, "objects")

			if err := exec.Command("find", objectsPath, "-type", "f", "-empty", "-delete").Run(); err != nil {
				return fmt.Errorf("failed to run find in %s: %w", objectsPath, err)
			}
		} else {
			// For unhandled errors, we return immediately without retrying.
			log.Printf("git fetch failed with unhandled error in %s: %v. Giving up without retry.", repoPath, err)
			return err
		}

		// Sleep before retrying
		time.Sleep(5 * time.Second)
	}

	// Final error log after exceeding the maximum number of retries
	log.Printf("git fetch failed permanently after %d retries in %s: %v", maxRetries, repoPath, err)
	return err
}
