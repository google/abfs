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
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/abfs/manifest"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

const (
	repoProjectsDir    = "projects"
	repoProjectsObjDir = "project-objects"
)

func (r *Repo) projectDotGitPath(proj *manifest.Project) string {
	return path.Join(r.dotRepo, repoProjectsDir, fmt.Sprintf("%s.git", proj.GetPath()))
}

func (r *Repo) projectObjectsPath(proj *manifest.Project) string {
	return path.Join(r.dotRepo, repoProjectsObjDir, fmt.Sprintf("%s.git", proj.Name))
}

func (r *Repo) projectRemote(p *manifest.Project) (*manifest.Remote, error) {
	var remote string
	if r.flatMan.Default != nil {
		remote = r.flatMan.Default.Remote
	}
	if p.Remote != "" {
		remote = p.Remote
	}
	result := r.flatMan.RemoteFromName(remote)
	if result == nil {
		return nil, fmt.Errorf("remote %s not found", remote)
	}
	return result, nil
}

func (r *Repo) projectRevision(p *manifest.Project) (string, error) {
	var revision string
	if r.flatMan.Default != nil {
		revision = r.flatMan.Default.Revision
	}
	remote, err := r.projectRemote(p)
	if err != nil {
		return "", err
	}
	if remote.Revision != "" {
		revision = remote.Revision
	}
	if p.Revision != "" {
		revision = p.Revision
	}
	return revision, nil
}

func (r *Repo) cloneProject(ctx context.Context, project *manifest.Project, opts SyncOpts) error {
	projectPath := path.Join(r.repoRoot, project.GetPath())

	remote, err := r.projectRemote(project)
	if err != nil {
		return err
	}

	remoteName := remote.Name
	if remote.Alias != "" {
		remoteName = remote.Alias
	}

	revision, err := r.projectRevision(project)
	if err != nil {
		return err
	}

	remoteURL, err := manifest.ExpandFetch(remote.Fetch, r.manifestURL)
	if err != nil {
		return err
	}

	projectURL, err := url.Parse(remoteURL)
	if err != nil {
		return err
	}
	projectURL.Path = path.Join(projectURL.Path, project.Name)

	gitDir := r.projectDotGitPath(project)
	objDir := path.Join(r.projectObjectsPath(project), "objects")

	// If the project's git directory already exists, this is a re-sync; update
	// the existing project rather than re-cloning it.
	if _, err := os.Stat(gitDir); err == nil {
		return r.updateProject(ctx, project, projectPath, gitDir, objDir, remoteName, revision, opts)
	} else if !entities.IsNotExist(err) {
		return fmt.Errorf("cannot stat git dir %s: %w", gitDir, err)
	}

	var cloneBranch string
	var cloneCommit entities.Hash
	if entities.IsHash(revision) {
		cloneCommit = entities.NewHash(revision)
	} else {
		cloneBranch = revision
	}
	err = r.Git.Clone(ctx, casgit.CloneOptions{
		RemoteRepo:       projectURL.String(),
		Directory:        projectPath,
		GitDir:           gitDir,
		ObjectsDir:       objDir,
		Origin:           remoteName,
		Branch:           cloneBranch,
		Commit:           cloneCommit,
		PrefetchIndices:  r.PrefetchIndices,
		PrefetchSymlinks: r.PrefetchSymlinks,
		Quiet:            true,
		SingleBranch:     true,
		NoTags:           true,
		DetachedHead:     true,
	})

	if err != nil {
		if !opts.FallbackToGit {
			return err
		}
		if fallbackErr := r.cloneProjectFallback(ctx, project, projectPath, gitDir, objDir, remoteName, revision, projectURL); fallbackErr != nil {
			return fmt.Errorf("project %s clone failed (%v) and fallback failed: %w", project.Name, err, fallbackErr)
		}
	}

	prj := casgit.NewGitRepo(r.Git, projectPath, gitDir, objDir)
	if err := prj.LoadConfig(); err != nil {
		return err
	}

	if remote.Review != "" {
		if err := prj.AddConfig(fmt.Sprintf("remote.%s.review", remoteName), remote.Review); err != nil {
			return err
		}
	}

	if err := r.symlink(gitDir, filepath.Join(projectPath, ".git"), true); err != nil {
		return err
	}

	if _, err := os.Stat(objDir); err == nil {
		if err := r.symlink(objDir, filepath.Join(gitDir, "objects"), true); err != nil {
			return err
		}
	} else if !entities.IsNotExist(err) {
		return fmt.Errorf("failed to stat objects dir %s: %w", objDir, err)
	}

	//TODO: LFS
	return nil
}

func (r *Repo) cloneProjectFallback(ctx context.Context, project *manifest.Project, projectPath, gitDir, objDir, remoteName, revision string, projectURL *url.URL) (returnErr error) {
	log.Printf("ABFS clone for %s failed, attempting fallback to git...", project.Name)

	// Clean up on exit if fallback fails completely
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(projectPath)
			_ = os.RemoveAll(gitDir)
		}
	}()

	// 1. First try optimized fallback with default branch or upstream
	defaultRev := ""
	if r.flatMan != nil && r.flatMan.Default != nil {
		defaultRev = r.flatMan.Default.Revision
	}
	fallbackBranch, hasFallback := determineFallbackBranch(project.Upstream, defaultRev, revision)

	if hasFallback {
		if err := r.tryOptimizedFallback(ctx, project, projectPath, gitDir, objDir, remoteName, revision, fallbackBranch, projectURL); err == nil {
			return nil
		} else {
			log.Printf("Optimized fallback for %s failed (%v), falling back to full git clone...", project.Name, err)
		}
	}

	// 2. Full bare clone fallback with worktree
	log.Printf("Optimized fallback for %s failed, falling back to full git clone...", project.Name)
	_ = os.RemoveAll(projectPath)
	_ = os.RemoveAll(gitDir)

	if err := os.MkdirAll(projectPath, 0755); err != nil {
		return fmt.Errorf("failed to create project directory %s: %w", projectPath, err)
	}
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		return fmt.Errorf("failed to create git dir %s: %w", gitDir, err)
	}

	// Exclude project and gitDir from CASFS cache so objects/packfiles are not uploaded
	// All files will be backed by the scratch directory of the mount.
	if err := r.CASFS.SetExcluded(projectPath, true); err != nil {
		return fmt.Errorf("failed to exclude project dir %s from cache: %w", projectPath, err)
	}
	if err := r.CASFS.SetExcluded(gitDir, true); err != nil {
		return fmt.Errorf("failed to exclude git dir %s from cache: %w", gitDir, err)
	}

	if err := r.shellGit.GitCloneBareWithWorktree(ctx, gitDir, projectPath, projectURL.String(), remoteName, revision); err != nil {
		return fmt.Errorf("failed to git clone %s for %s: %w", revision, project.Name, err)
	}

	log.Printf("Checked out %s from git fallback.", project.Name)
	return nil
}

func (r *Repo) tryOptimizedFallback(ctx context.Context, project *manifest.Project, projectPath, gitDir, objDir, remoteName, revision, fallbackBranch string, projectURL *url.URL) error {
	_ = os.RemoveAll(projectPath)
	_ = os.RemoveAll(gitDir)

	if err := r.Git.Clone(ctx, casgit.CloneOptions{
		RemoteRepo:       projectURL.String(),
		Directory:        projectPath,
		GitDir:           gitDir,
		ObjectsDir:       objDir,
		Origin:           remoteName,
		Branch:           fallbackBranch,
		PrefetchIndices:  r.PrefetchIndices,
		PrefetchSymlinks: r.PrefetchSymlinks,
		Quiet:            true,
		SingleBranch:     true,
		NoTags:           true,
		DetachedHead:     true,
	}); err != nil {
		return fmt.Errorf("base clone failed: %w", err)
	}

	// Ensure worktree and symlinks are configured so git checkout FETCH_HEAD works into projectPath
	_ = r.symlink(gitDir, filepath.Join(projectPath, ".git"), true)
	if _, statErr := os.Stat(objDir); statErr == nil {
		_ = r.symlink(objDir, filepath.Join(gitDir, "objects"), true)
	}
	_ = r.shellGit.AddConfig(gitDir, "core.worktree", projectPath)

	fetchCmd := exec.CommandContext(ctx, r.shellGit.BinPath, "--git-dir="+gitDir, "fetch", remoteName, revision)
	if fetchErr := fetchCmd.Run(); fetchErr != nil {
		return fmt.Errorf("git fetch failed: %w", fetchErr)
	}

	checkoutCmd := exec.CommandContext(ctx, r.shellGit.BinPath, "--git-dir="+gitDir, "--work-tree="+projectPath, "checkout", "-f", "--detach", "FETCH_HEAD")
	if checkoutErr := checkoutCmd.Run(); checkoutErr != nil {
		return fmt.Errorf("git checkout failed: %w", checkoutErr)
	}

	log.Printf("Optimized git fallback for %s succeeded.", project.Name)
	return nil
}

func determineFallbackBranch(upstream, defaultRevision, revision string) (string, bool) {
	branch := ""
	if upstream != "" && !entities.IsHash(upstream) {
		branch = upstream
	} else if defaultRevision != "" && !entities.IsHash(defaultRevision) {
		branch = defaultRevision
	}
	if branch == "HEAD" {
		branch = ""
	}

	if revision != branch {
		return branch, true
	}
	if entities.IsHash(revision) {
		return "", true
	}
	return "", false
}

func (r *Repo) updateManifest(ctx context.Context, fallbackToGit bool) error {
	if strings.TrimPrefix(r.manifestBranch, "refs/heads/") == "" {
		return nil
	}
	gitDir := path.Join(r.dotRepo, "manifests.git")
	objDir := path.Join(gitDir, "objects")
	// origin should be the remote name because manifest itself couldn't specify remote name
	gitRepo := casgit.NewGitRepo(r.Git, r.manifestsDir, gitDir, objDir)
	err := r.updateGitRepo(ctx, gitRepo, "manifests", r.manifestsDir, gitDir, objDir, "origin", r.manifestBranch, SyncOpts{})
	if err != nil && fallbackToGit {
		log.Printf("ABFS update for manifests failed (%v), falling back to git...", err)
		if fetchErr := r.shellGit.Fetch(gitDir); fetchErr != nil {
			log.Printf("Warning: git fetch fallback for manifests failed: %v", fetchErr)
		}
		return r.shellGit.Checkout(r.manifestsDir, r.manifestBranch)
	}
	return err
}

func (r *Repo) updateProject(ctx context.Context, project *manifest.Project, workDir, gitDir, objDir, remoteName, revision string, opts SyncOpts) error {
	gitRepo := casgit.NewGitRepo(r.Git, workDir, gitDir, objDir)
	err := r.updateGitRepo(ctx, gitRepo, project.Name, workDir, gitDir, objDir, remoteName, revision, opts)
	if err != nil && opts.FallbackToGit {
		log.Printf("ABFS update for %s failed (%v), falling back to git...", project.Name, err)
		if fetchErr := r.shellGit.Fetch(gitDir); fetchErr != nil {
			log.Printf("Warning: git fetch fallback for %s failed: %v", project.Name, fetchErr)
		}
		return r.shellGit.Checkout(workDir, revision)
	}
	return err
}

func (r *Repo) updateGitRepo(ctx context.Context, gitRepo *casgit.GitRepo, projectName, workDir, gitDir, objDir, remoteName, revision string, opts SyncOpts) error {
	if err := gitRepo.Load(); err != nil {
		return fmt.Errorf("failed to load git repo: %w", err)
	}

	fetch := &casgit.GitFetch{
		FetchOptions: casgit.FetchOptions{
			Remote:    remoteName,
			GitDir:    gitDir,
			ObjectDir: objDir,
			NoTags:    true,
			Quiet:     true,
		},
		GitRepo: gitRepo,
	}

	if err := fetch.Fetch(ctx); err != nil {
		return fmt.Errorf("failed to fetch project %s: %w", projectName, err)
	}

	if !entities.IsHash(revision) {
		if strings.HasPrefix(revision, "refs") {
			revision = strings.TrimPrefix(revision, "refs/heads/")
			revision = strings.TrimPrefix(revision, "refs/tags/")
		}
		revision = fmt.Sprintf("refs/remotes/%s/%s", remoteName, revision)
	}

	localCommits := []string{}
	localBranchName := ""

	if !gitRepo.IsDetached() {
		if branch, err := gitRepo.CurrentBranch(); err == nil {
			localBranchName = branch.Name
			localCommits, err = gitRepo.LocalCommits(localBranchName)
			if err != nil {
				return err
			}
		}
	}

	hasUncommited, err := gitRepo.HasUncommitedChanges()
	if err != nil {
		return fmt.Errorf("failed to check uncommitted changes for project %s: %w", projectName, err)
	}

	// failed if there there are uncommited files on a branch
	if localBranchName != "" && hasUncommited {
		return fmt.Errorf("project %s contains uncommitted changes", projectName)
	}

	checkout := &casgit.GitCheckout{
		CheckoutOptions: casgit.CheckoutOptions{
			Target:    revision,
			GitDir:    gitDir,
			ObjectDir: objDir,
			WorkDir:   workDir,
			Detach:    true,
			Force:     true,
			Quiet:     true,
		},
		GitRepo: gitRepo,
	}

	if err := checkout.Checkout(ctx); err != nil {
		return fmt.Errorf("failed to checkout project %s at %s: %w", projectName, revision, err)
	}

	// Ensure the .git and objects symlinks are still in place (idempotent).
	tuple := [][]string{
		{gitDir, filepath.Join(workDir, ".git")},
		{objDir, filepath.Join(gitDir, "objects")},
	}
	for _, t := range tuple {
		target, path := t[0], t[1]
		if _, err := os.Lstat(path); err != nil && !entities.IsNotExist(err) {
			return err
		} else if entities.IsNotExist(err) {
			if err := r.symlink(target, path, true); err != nil {
				return err
			}
		}
	}

	if localBranchName != "" {
		// Just hard reset if there are no rebase flag
		if len(localCommits) > 0 && opts.Rebase {
			if err := gitRepo.RebaseCommitsOnto(projectName, localCommits, revision); err != nil {
				return err
			}
		}

		if err := gitRepo.UpdateLocalBranchToHead(projectName, localBranchName); err != nil {
			return err
		}
	}

	return nil
}

func (r *Repo) createSymlinks(project *manifest.Project) error {
	for _, link := range project.Linkfile {
		target := path.Join(project.GetPath(), link.Src)
		sym := path.Join(r.repoRoot, link.Dest)
		if err := r.symlink(target, sym, false); err != nil {
			if entities.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("cannot create symlink %s pointing to %s: %w", sym, target, err)
		}
	}
	return nil
}

func (r *Repo) linkShareableDir(project *manifest.Project) error {
	repoShareableDirs := []string{
		"hooks",
		"rr-cache",
		"objects",
	}

	for _, dir := range repoShareableDirs {
		old := path.Join(r.projectObjectsPath(project), dir)
		newPath := path.Join(r.projectDotGitPath(project), dir)

		if _, err := os.Stat(old); err != nil {
			if entities.IsNotExist(err) {
				continue
			}
			return err
		}

		rel, err := filepath.Rel(filepath.Dir(newPath), old)
		if err != nil {
			return err
		}

		if existing, err := os.Readlink(newPath); err == nil && existing == rel {
			continue
		}

		if err := os.RemoveAll(newPath); err != nil {
			return err
		}
		if err := os.Symlink(rel, newPath); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) copyFiles(project *manifest.Project) error {
	for _, copyFile := range project.Copyfile {
		srcFilePath := path.Join(r.repoRoot, project.GetPath(), copyFile.Src)
		dstFilePath := path.Join(r.repoRoot, copyFile.Dest)

		dstDir := path.Dir(dstFilePath)
		if err := os.MkdirAll(dstDir, 0755); err != nil {
			return fmt.Errorf("cannot create dir %s: %w", dstDir, err)
		}

		stat, err := r.CASFS.HashStat(srcFilePath)
		if err != nil {
			// If file exists on disk (e.g. from git fallback), copy using standard file I/O
			if content, readErr := os.ReadFile(srcFilePath); readErr == nil {
				if writeErr := os.WriteFile(dstFilePath, content, 0644); writeErr != nil {
					return fmt.Errorf("cannot write copyfile %s: %w", dstFilePath, writeErr)
				}
				continue
			}
			if entities.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("cannot stat source file of copyfile %s: %w", srcFilePath, err)
		}

		if err := os.Remove(dstFilePath); err != nil && !entities.IsNotExist(err) {
			return fmt.Errorf("cannot remove existing copyfile %s: %w", dstFilePath, err)
		}

		if err := r.CASFS.Mknod(dstFilePath, stat.Mode, 0); err != nil {
			return fmt.Errorf("cannot create the target file of copyfile %s: %w", dstFilePath, err)
		}

		if err = r.CASFS.SetHashSize(dstFilePath, stat.Hash, stat.Size); err != nil {
			return fmt.Errorf("cannot set attribute from %s to %s: %w", srcFilePath, dstFilePath, err)
		}
	}
	return nil
}

func (r *Repo) symlink(target, path string, exclude bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(r.repoRoot, target)
	}
	rel, err := filepath.Rel(filepath.Dir(path), target)
	if err != nil {
		return err
	}
	if existing, err := os.Readlink(path); err == nil {
		if existing == rel {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !entities.IsNotExist(err) {
		// path exists but is not a symlink; remove it so we can replace it.
		if err := os.Remove(path); err != nil {
			return err
		}
	}

	if err := os.Symlink(rel, path); err != nil {
		return err
	}
	if exclude {
		if err := bindcas.SetExcluded(path, true); err != nil {
			return fmt.Errorf("failed to exclude file %s: %w", path, err)
		}
	}
	return nil
}

func (r *Repo) createCommitHook(project *manifest.Project) error {
	target := path.Join(r.projectObjectsPath(project), "hooks", "commit-msg")
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !entities.IsNotExist(err) {
		return fmt.Errorf("cannot check existence of target file of hook %s: %w", target, err)
	}

	source := path.Join(r.dotRepo, "repo", "hooks", "commit-msg")
	if _, err := os.Stat(source); entities.IsNotExist(err) {
		return nil
	}
	hash, err := r.CASFS.Hash(source)
	if err != nil {
		return fmt.Errorf("cannot geth hash of %s: %w", target, err)
	}

	if err := os.MkdirAll(path.Dir(target), 0755); err != nil {
		return err
	}
	if err := r.CASFS.Mknod(target, entities.FileModeExecutable, 0); err != nil {
		return fmt.Errorf("cannot create the target file of copyfile %s: %w", target, err)
	}

	return r.CASFS.SetHash(target, hash)
}
