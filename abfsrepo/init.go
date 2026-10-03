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

package abfsrepo

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/entities"
)

const TraceFile = "TRACE_FILE"

type InitOpts struct {
	RepoURL        string
	RepoRev        string
	ManifestName   string
	ManifestBranch string
	ManifestURL    string
	Groups         []string
	FallbackToGit  bool
}

func (c *Repo) Init(ctx context.Context, opts InitOpts) error {
	c.manifestURL = opts.ManifestURL
	c.manifestBranch = opts.ManifestBranch
	c.manifestRemote = "origin"

	if err := c.fetchRepo(ctx, opts.RepoURL, opts.RepoRev, opts.FallbackToGit); err != nil {
		return err
	}

	c.downloadCommitMsgHook(ctx)

	if err := c.fetchManifest(ctx, opts.FallbackToGit); err != nil {
		return err
	}
	if err := c.manifestProject.Load(); err != nil {
		return err
	}

	if len(opts.Groups) > 0 {
		if err := c.manifestProject.AddConfig(ConfigManifestGroups, strings.Join(opts.Groups, ",")); err != nil {
			return err
		}
	}

	if err := c.manifestProject.AddConfig(ConfigSyncRemoteURL, c.manifestURL); err != nil {
		return err
	}
	if err := c.manifestProject.AddConfig(ConfigSyncDefaultRemote, "origin"); err != nil {
		return err
	}
	if err := c.manifestProject.AddConfig(ConfigSyncDefaultMerge, "refs/heads/"+c.manifestBranch); err != nil {
		return err
	}

	if err := c.writeMasterManifest(opts.ManifestName); err != nil {
		return err
	}

	if err := c.touchTraceFile(); err != nil {
		return err
	}

	return nil
}

// Repo creates a trace file for tracking gitops related operations.
// It gets updated for each git command and is only used for debugging/profiling, so it is okay for
// it to not be included in the snapshot.
func (r *Repo) touchTraceFile() error {
	p := path.Join(r.dotRepo, TraceFile)
	file, err := os.OpenFile(p, os.O_RDONLY|os.O_CREATE, 0664)
	if err != nil {
		return fmt.Errorf("failed to touch trace file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close trace file: %w", err)
	}
	return r.CASFS.SetExcluded(p, true)
}

// TODO: do check the default branch
func (c *Repo) fetchRepo(ctx context.Context, repoURL, repoRev string, fallbackToGit bool) error {
	repoBinDir := path.Join(c.dotRepo, "repo")
	if _, err := os.Lstat(repoBinDir); err == nil {
		//TODO update repo accordingly
		return nil
	}

	err := c.Git.Clone(ctx, casgit.CloneOptions{
		Directory:        repoBinDir,
		Origin:           "origin",
		Branch:           repoRev,
		RemoteRepo:       repoURL,
		PrefetchIndices:  c.PrefetchIndices,
		PrefetchSymlinks: c.PrefetchSymlinks,
	})

	if err != nil {
		if !fallbackToGit {
			return err
		}
		log.Printf("ABFS clone for repo tools failed (%v), falling back to git...", err)
		_ = c.CASFS.SetExcluded(repoBinDir, true)
		if cloneErr := c.shellGit.GitCloneBranchAt(ctx, repoBinDir, repoURL, repoRev); cloneErr != nil {
			log.Printf("Warning: cloning git-repo tools via git fallback failed: %v", cloneErr)
		}
	}
	return nil
}

func (c *Repo) fetchManifest(ctx context.Context, fallbackToGit bool) error {
	manifestPath := path.Join(c.dotRepo, "manifests")
	manifestDotGit := path.Join(c.dotRepo, "manifests.git")
	if _, err := os.Lstat(manifestPath); err == nil {
		//TODO update repo accordingly
		return c.updateManifest(ctx, fallbackToGit)
	} else if !entities.IsNotExist(err) {
		return err
	}

	branch := c.manifestBranch
	if branch == "HEAD" {
		branch = ""
	}

	var cloneBranch string
	var cloneCommit entities.Hash
	if entities.IsHash(branch) {
		cloneCommit = entities.NewHash(branch)
	} else {
		cloneBranch = branch
	}

	err := c.Git.Clone(ctx, casgit.CloneOptions{
		Directory:        manifestPath,
		GitDir:           manifestDotGit,
		Origin:           "origin",
		Branch:           cloneBranch,
		Commit:           cloneCommit,
		RemoteRepo:       c.manifestURL,
		PrefetchIndices:  c.PrefetchIndices,
		PrefetchSymlinks: c.PrefetchSymlinks,
	})

	if err != nil {
		if !fallbackToGit {
			return err
		}

		log.Printf("ABFS clone for manifests failed (%v), falling back to git...", err)
		_ = os.RemoveAll(manifestPath)
		_ = os.RemoveAll(manifestDotGit)

		if err := os.MkdirAll(manifestPath, 0755); err != nil {
			return fmt.Errorf("failed to create manifests dir: %w", err)
		}
		if err := os.MkdirAll(manifestDotGit, 0755); err != nil {
			return fmt.Errorf("failed to create directory for manifests.git: %w", err)
		}
		if err := c.CASFS.SetExcluded(manifestPath, true); err != nil {
			return fmt.Errorf("failed to exclude manifests dir from cache: %w", err)
		}
		if err := c.CASFS.SetExcluded(manifestDotGit, true); err != nil {
			return fmt.Errorf("failed to exclude manifests.git from cache: %w", err)
		}

		manifestRef := c.manifestBranch
		if manifestRef == "" {
			manifestRef = "HEAD"
		}
		if cloneErr := c.shellGit.GitCloneBareWithWorktree(ctx, manifestDotGit, manifestPath, c.manifestURL, "origin", manifestRef); cloneErr != nil {
			_ = os.RemoveAll(manifestPath)
			_ = os.RemoveAll(manifestDotGit)
			return fmt.Errorf("ABFS manifest clone failed (%v) and git fallback failed: %w", err, cloneErr)
		}
	}

	linkedDotGit := path.Join(manifestPath, ".git")
	linkTarget, err := filepath.Rel(manifestPath, manifestDotGit)
	if err != nil {
		return err
	}
	if existing, err := os.Readlink(linkedDotGit); err == nil {
		if existing != linkTarget {
			_ = os.Remove(linkedDotGit)
			if err := os.Symlink(linkTarget, linkedDotGit); err != nil {
				return err
			}
		}
	} else if os.IsNotExist(err) {
		if err := os.Symlink(linkTarget, linkedDotGit); err != nil {
			return err
		}
	} else {
		_ = os.Remove(linkedDotGit)
		if err := os.Symlink(linkTarget, linkedDotGit); err != nil {
			return err
		}
	}

	if err := c.CASFS.SetExcluded(linkedDotGit, true); err != nil {
		return err
	}

	return nil
}

func (c *Repo) writeMasterManifest(includeName string) error {
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!--
DO NOT EDIT THIS FILE!  It is generated by repo and changes will be discarded.
If you want to use a different manifest, use 'repo init -m <file>' instead.

If you want to customize your checkout by overriding manifest settings, use
the local_manifests/ directory instead.

For more information on repo manifests, check out:
https://gerrit.googlesource.com/git-repo/+/HEAD/docs/manifest-format.md
-->
<manifest>
  <include name="%s" />
</manifest>
`,
		includeName,
	)
	return os.WriteFile(path.Join(c.dotRepo, "manifest.xml"), []byte(content), 0644)
}

func (c *Repo) downloadCommitMsgHook(ctx context.Context) {
	casgit.SetupCentralCommitMsgHook(ctx, c.Store, c.CASFS, c.repoRoot)
}
