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

package repo

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/pflag"

	"github.com/google/abfs/abfsrepo"
	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
)

type syncCmd struct {
	*repoCmd
	abfsrepo.SyncOpts

	repo *abfsrepo.Repo
}

func newSyncCmd(c *repoCmd) *syncCmd {
	return &syncCmd{repoCmd: c}
}

func (c *syncCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "sync",
		Usage: "[options] [<project>...]",
		Desc:  `Update working tree to the latest revision`,
	}
}

func (c *syncCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.IntVarP(&c.Jobs, "jobs", "j", 0, "Number of jobs to run in parallel. Zero uses the number of CPU cores")
	fl.BoolVar(&c.Rebase, "rebase", false, "Rebase local commits on top of new upstream")
	fl.BoolVarP(&c.SmartSync, "smart-sync", "s", false, "Sync to a known good revision as specified by the manifest server")
	fl.StringVarP(&c.SmartTag, "smart-tag", "t", "", "Sync to a tag as specified by the manifest server")
	fl.StringVar(&c.ManifestServerUsername, "manifest-server-username", "", "Username to authenticate with the manifest server")
	fl.StringVar(&c.ManifestServerPassword, "manifest-server-password", "", "Password to authenticate with the manifest server")
	fl.BoolVar(&c.FallbackToGit, "fallback-to-git", false, "Fallback to git operation directly if mount fails on a project.")

	// Placeholder flags to accept all git-repo sync flags
	fl.Int("jobs-network", 0, "")
	fl.Int("jobs-checkout", 0, "")
	fl.Bool("fail-fast", false, "")
	fl.Bool("force-sync", false, "")
	fl.Bool("force-checkout", false, "")
	fl.Bool("force-remove-dirty", false, "")
	fl.BoolP("local-only", "l", false, "")
	fl.Bool("no-manifest-update", false, "")
	fl.Bool("nmu", false, "")
	fl.Bool("interleaved", false, "")
	fl.Bool("no-interleaved", false, "")
	fl.BoolP("network-only", "n", false, "")
	fl.BoolP("detach", "d", false, "")
	fl.BoolP("current-branch", "c", false, "")
	fl.Bool("no-current-branch", false, "")
	fl.StringP("manifest-name", "m", "", "")
	fl.Bool("clone-bundle", false, "")
	fl.Bool("no-clone-bundle", false, "")
	fl.Bool("fetch-submodules", false, "")
	fl.Bool("use-superproject", false, "")
	fl.Bool("no-use-superproject", false, "")
	fl.String("superproject-revision", "", "")
	fl.Bool("tags", false, "")
	fl.Bool("no-tags", false, "")
	fl.Bool("optimized-fetch", false, "")
	fl.Bool("retry-fetches", false, "")
	fl.Bool("prune", false, "")
	fl.Bool("no-prune", false, "")
	fl.Bool("auto-gc", false, "")
	fl.Bool("no-auto-gc", false, "")
	fl.BoolP("force-broken", "f", false, "")
}

func (c *syncCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	implementedFlags := map[string]bool{
		"jobs":                     true,
		"rebase":                   true,
		"smart-sync":               true,
		"smart-tag":                true,
		"manifest-server-username": true,
		"manifest-server-password": true,
		"fallback-to-git":          true,
	}
	fl.Visit(func(f *pflag.Flag) {
		if !implementedFlags[f.Name] {
			log.Printf("warning: flag --%s is not implemented in abfs repo sync yet", f.Name)
		}
	})

	if c.Jobs == 0 {
		c.Jobs = runtime.NumCPU()
	}
	return c.exec(ctx, fl.Args())
}

func (c *syncCmd) exec(ctx context.Context, projects []string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	cleanup, err := c.setupCAS()
	if err != nil {
		return err
	}
	defer cleanup()

	opts := abfsrepo.RepoOpts{
		Store:            c.Store,
		Git:              c.casgit,
		CASFS:            c.casfs,
		Client:           c.client,
		PrefetchIndices:  c.Config.GitPrefetchIndices,
		PrefetchSymlinks: c.Config.GitPrefetchSymlinks,
	}
	repo, err := abfsrepo.NewRepo(opts, dir)
	if errors.Is(err, abfsrepo.ErrNotRepo) {
		return clierr.CLIErr(`error: command 'sync' requires repo to be installed first.
        Use "repo init" to install it here.`)
	}
	if err != nil {
		return err
	}
	c.repo = repo

	if err := repo.Load(ctx); err != nil {
		return err
	}

	if err := repo.Sync(ctx, projects, c.SyncOpts); err != nil {
		return err
	}

	if err := binding.PrefetchDirs(c.Config.SyncPrefetchDirs, c.Cacheman, binding.ContentPrefetchPriority); err != nil {
		log.Printf("Content prefetch failed: %v", err)
	}

	if c.Config.SyncAutoSetup {
		if err := c.autoSetup(ctx); err != nil {
			return clierr.CLIErr("auto-setup failed: %v", err)
		}
	}

	if notice := repo.ManifestNotice(); notice != "" {
		fmt.Println()
		fmt.Println(notice)
	}

	fmt.Println("\nrepo sync has finished successfully.")

	return nil
}

func (c *syncCmd) autoSetup(ctx context.Context) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"setup", "."}
	if c.client.Root() != c.repo.Root() {
		chroot, err := filepath.Rel(c.client.Root(), c.repo.Root())
		if err != nil {
			return err
		}

		args = append(args, "--chroot", chroot)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to auto-setup: %v", err)
	}
	fmt.Println("ABFS setup completed successfully.")
	return nil
}
