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
	"maps"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/manifest"

	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

type SyncOpts struct {
	Jobs                   int
	Rebase                 bool
	SmartSync              bool
	SmartTag               string
	ManifestServerUsername string
	ManifestServerPassword string
	FallbackToGit          bool
}

func (r *Repo) Sync(ctx context.Context, projectsToSync []string, opts SyncOpts) error {
	defer func() {
		if err := r.writeRepoFiles(); err != nil {
			fmt.Printf("Error writing repo files: %v\n", err)
		}
	}()

	// Always delete the override manifest at the start of sync
	// to ensure we load the original manifest first.
	overridePath := path.Join(r.manifestsDir, "smart_sync_override.xml")
	if err := os.Remove(overridePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove smart sync override: %w", err)
	}

	if err := r.fetchManifest(ctx, opts.FallbackToGit); err != nil {
		return err
	}

	if err := r.Load(ctx); err != nil {
		return err
	}

	// Apply smart manifest if requested
	if opts.SmartSync || opts.SmartTag != "" {
		if err := r.applySmartManifest(ctx, opts); err != nil {
			return fmt.Errorf("failed to apply smart manifest: %w", err)
		}
		// Re-load the manifest after applying the smart one (which will now load the override)
		if err := r.Load(ctx); err != nil {
			return fmt.Errorf("failed to load smart manifest: %w", err)
		}
	}

	return r.sync(ctx, projectsToSync, opts)
}

func (r *Repo) sync(ctx context.Context, projectsToSync []string, opts SyncOpts) error {
	if len(projectsToSync) == 0 {
		projectsToSync = slices.Collect(maps.Keys(r.projectsByPath))
	}

	toSync := make(map[string]struct{}, len(projectsToSync))
	for _, t := range projectsToSync {
		if p, ok := r.projectsByPath[t]; ok {
			toSync[p.Name] = struct{}{}
			continue
		}
		if p, ok := r.projectsByName[t]; ok {
			toSync[p.Name] = struct{}{}
			continue
		}

		if relPath, err := r.RelativeToRoot(t); err == nil {
			if p, ok := r.projectsByPath[relPath]; ok {
				toSync[p.Name] = struct{}{}
				continue
			}
		}

		return fmt.Errorf("project %s not found", t)
	}

	if err := r.syncRemoves(); err != nil {
		return err
	}

	progress := NewProgress("Syncing:", len(toSync))
	defer progress.Stop()

	var lock sync.Mutex
	var failedErrors []error
	var failedProjects []string
	var synced []*manifest.Project
	limit := opts.Jobs
	if limit <= 0 {
		limit = runtime.NumCPU()
	}
	for _, level := range r.checkoutLevels(toSync) {
		grp, gctx := errgroup.WithContext(ctx)
		grp.SetLimit(limit)
		for _, p := range level {
			grp.Go(func() error {
				project := r.projectsByName[p]
				if project == nil {
					lock.Lock()
					errWithPath := fmt.Errorf("project %s not found", p)
					failedErrors = append(failedErrors, errWithPath)
					failedProjects = append(failedProjects, p)
					lock.Unlock()
					return nil
				}
				progress.StartTask(project.GetPath())
				err := r.syncProject(gctx, project, opts)
				progress.EndTask(project.GetPath(), err == nil)
				if err != nil {
					lock.Lock()
					errWithName := fmt.Errorf("project %s: %w", p, err)
					failedErrors = append(failedErrors, errWithName)
					failedProjects = append(failedProjects, p)
					lock.Unlock()
					return nil
				}

				lock.Lock()
				r.curProjects[project.GetPath()] = struct{}{}
				synced = append(synced, project)
				lock.Unlock()
				return nil
			})
		}

		if err := grp.Wait(); err != nil {
			return err
		}
	}

	errs, failed := r.finishCheckouts(synced)
	failedErrors = append(failedErrors, errs...)
	failedProjects = append(failedProjects, failed...)

	if len(failedProjects) > 0 {
		for _, e := range failedErrors {
			fmt.Fprintf(os.Stderr, "%s\n", e)
		}
		fmt.Fprintf(os.Stderr, "error: Exited sync due to syncing errors\n")
		fmt.Fprintf(os.Stderr, "Failing projects:\n")
		for _, p := range failedProjects {
			fmt.Fprintf(os.Stderr, "%s\n", p)
		}
		return clierr.CLIErr("failed to sync projects")
	}

	return nil
}

// finishCheckouts makes the link and copy files of the checked-out projects after every checkout,
// as a destination may lie inside the path of a project checked out later. Then it checks that
// every work tree is still there. It returns the errors and the failing projects.
func (r *Repo) finishCheckouts(synced []*manifest.Project) ([]error, []string) {
	var errs []error
	var failed []string
	slices.SortFunc(synced, func(a, b *manifest.Project) int { return strings.Compare(a.GetPath(), b.GetPath()) })
	for _, project := range synced {
		if err := r.syncProjectFiles(project); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", project.Name, err))
			failed = append(failed, project.Name)
		}
	}
	for _, project := range synced {
		if err := r.checkWorkTree(project); err != nil {
			delete(r.curProjects, project.GetPath())
			if !slices.Contains(failed, project.Name) {
				failed = append(failed, project.Name)
			}
			errs = append(errs, err)
		}
	}
	return errs, failed
}

// checkoutLevels splits the project names into levels by path, as repo orders its checkouts: a
// project whose path lies inside another project's path comes in a later level.
func (r *Repo) checkoutLevels(names map[string]struct{}) [][]string {
	byPath := make(map[string]string, len(names))
	for name := range names {
		projectPath := name
		if project := r.projectsByName[name]; project != nil {
			projectPath = project.GetPath()
		}
		byPath[projectPath] = name
	}
	var levels [][]string
	for _, paths := range PathLevels(slices.Collect(maps.Keys(byPath))) {
		level := make([]string, 0, len(paths))
		for _, p := range paths {
			level = append(level, byPath[p])
		}
		levels = append(levels, level)
	}
	return levels
}

func (r *Repo) syncRemoves() error {
	if err := r.syncProjectRemoves(); err != nil {
		return err
	}
	if err := r.syncCopyFileRemoves(); err != nil {
		return err
	}
	if err := r.syncLinkFileRemoves(); err != nil {
		return err
	}
	return nil
}

func (r *Repo) syncProjectRemoves() error {
	//TODO remove .git and objects
	toRemove := make([]string, 0)
	for p := range r.curProjects {
		if _, ok := r.projectsByPath[p]; !ok {
			toRemove = append(toRemove, p)
		}
	}
	// Deepest first, as repo removes obsolete projects, so an inner project goes before the
	// project around it.
	slices.Sort(toRemove)
	slices.Reverse(toRemove)
	for _, p := range toRemove {
		if err := removeWorkTree(path.Join(r.repoRoot, p)); err != nil {
			return err
		}
		delete(r.curProjects, p)
	}
	return nil
}

// removeWorkTree removes a work tree except the directories that hold another project's .git,
// as repo deletes an obsolete checkout, so projects inside it and their changes stay.
func removeWorkTree(dir string) error {
	entries, err := os.ReadDir(dir)
	if entities.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	kept := false
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		if !e.IsDir() {
			if err := os.Remove(p); err != nil && !entities.IsNotExist(err) {
				return err
			}
			continue
		}
		if _, err := os.Lstat(path.Join(p, ".git")); err == nil {
			kept = true
			continue
		}
		if err := removeWorkTree(p); err != nil {
			return err
		}
		if _, err := os.Lstat(p); err == nil {
			kept = true
		}
	}
	if kept {
		return nil
	}
	return os.Remove(dir)
}

// checkWorkTree fails when the project's work tree has no .git.
func (r *Repo) checkWorkTree(project *manifest.Project) error {
	if _, err := os.Lstat(path.Join(r.repoRoot, project.GetPath(), ".git")); err != nil {
		return fmt.Errorf("project %s: work tree %s is missing after sync: %w", project.Name, project.GetPath(), err)
	}
	return nil
}

func (r *Repo) syncCopyFileRemoves() error {
	oldCopyFiles := maps.Clone(r.curCopyFiles)

	for _, p := range r.projectsByName {
		for _, c := range p.Copyfile {
			delete(oldCopyFiles, c.Dest)
		}
	}

	for c := range oldCopyFiles {
		if err := os.Remove(path.Join(r.repoRoot, c)); err != nil {
			if !entities.IsNotExist(err) {
				return err
			}
		}
		delete(r.curCopyFiles, c)
	}

	return nil
}

func (r *Repo) syncLinkFileRemoves() error {
	oldLinkFiles := maps.Clone(r.curLinkFiles)

	for _, p := range r.projectsByName {
		for _, c := range p.Linkfile {
			delete(oldLinkFiles, c.Dest)
		}
	}

	for c := range oldLinkFiles {
		if err := os.Remove(path.Join(r.repoRoot, c)); err != nil {
			if !entities.IsNotExist(err) {
				return err
			}
		}
		delete(r.curLinkFiles, c)
	}

	return nil
}

func (r *Repo) syncProject(ctx context.Context, project *manifest.Project, opts SyncOpts) error {
	if err := r.cloneProject(ctx, project, opts); err != nil {
		return err
	}

	if err := r.createCommitHook(project); err != nil {
		return fmt.Errorf("failed to create symlink for commit-msg hook: %w", err)
	}

	if err := r.linkShareableDir(project); err != nil {
		return fmt.Errorf("error while link repo shareable dir: %w", err)
	}

	return nil
}

func (r *Repo) syncProjectFiles(project *manifest.Project) error {
	if err := r.createSymlinks(project); err != nil {
		return fmt.Errorf("error while creating the manifest symlinks: %w", err)
	}

	if err := r.copyFiles(project); err != nil {
		return fmt.Errorf("error while creating the manifest copyfiles: %w", err)
	}

	return nil
}
