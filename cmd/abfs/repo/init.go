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
	"os"
	"path"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/google/abfs/abfsrepo"
	"github.com/google/abfs/daemon"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
)

const (
	defaultRepoURL    = "https://gerrit.googlesource.com/git-repo"
	defaultRepoBranch = "stable-abfs"
)

type initCmd struct {
	*repoCmd
	abfsrepo.InitOpts
}

func newInitCmd(c *repoCmd) *initCmd {
	return &initCmd{repoCmd: c}
}

func (c *initCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "init",
		Usage: "[options] [-u] url",
		Desc:  `Initialize a repo client checkout in the current directory`,
	}
}

func GetEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func (c *initCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.RepoURL, "repo-url", "", GetEnvOrDefault("REPO_URL", defaultRepoURL), "repo repository location ($REPO_URL)")
	fl.StringVarP(&c.RepoRev, "repo-rev", "", GetEnvOrDefault("REPO_REV", defaultRepoBranch), "repo branch or revision ($REPO_REV)")
	fl.StringVarP(&c.ManifestName, "manifest-name", "m", "default.xml", "initial manifest file")
	fl.StringVarP(&c.ManifestBranch, "manifest-branch", "b", "", "manifest branch or revision")
	fl.StringVarP(&c.ManifestURL, "manifest-url", "u", "", "manifest repository location")
	fl.StringSliceVarP(&c.Groups, "groups", "g", nil, "restrict manifest projects to ones with specified group(s)")
	fl.BoolVar(&c.FallbackToGit, "fallback-to-git", false, "Fallback to git operation directly if clone fails on manifest.")
}

func (c *initCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	url := args.SingleArgOrDefault(fl, "")
	if c.ManifestURL == "" {
		c.ManifestURL = url
	}
	if c.ManifestURL == "" {
		return clierr.CLIErr("Manifest URL is required. Use --manifest-url or -u option to specify the manifest URL.")
	}

	cleanup, err := c.setupCAS()
	if err != nil {
		return err
	}
	defer cleanup()

	return c.exec(ctx)
}

func (c *initCmd) exec(ctx context.Context) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	root, err := abfsrepo.FindRepoRoot(wd)
	if err != nil && !errors.Is(err, abfsrepo.ErrNotRepo) {
		return err
	} else if err == nil {
		fmt.Printf("repo: reusing existing repo client checkout in %s\n", root)
	} else {
		root = wd
	}

	opts := abfsrepo.RepoOpts{
		Store:            c.Store,
		Git:              c.casgit,
		CASFS:            c.casfs,
		Client:           c.client,
		PrefetchIndices:  c.Config.GitPrefetchIndices,
		PrefetchSymlinks: c.Config.GitPrefetchSymlinks,
	}
	repo := abfsrepo.NewRepoForceRoot(opts, root)

	if err := repo.Init(ctx, c.InitOpts); err != nil {
		return err
	}

	// Soong expects the mount-details file to be at .repo/mount-details, so create a symlink to it in the repo directory.
	if err := c.createMountDetailsSym(repo); err != nil {
		return fmt.Errorf("failed to create symlink for mount details file: %w", err)
	}

	return nil
}

func (c *initCmd) createMountDetailsSym(repo *abfsrepo.Repo) error {
	target, err := filepath.Abs(path.Join(c.client.Root(), daemon.MountDetailsFileName))
	if err != nil {
		return err
	}
	link := path.Join(repo.Root(), ".repo", "mount-details")
	if _, err := os.Lstat(link); err == nil {
		// Symlink already exists, no need to create it again.
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat existing mount details symlink: %w", err)
	}

	// Create a relative symlink from the repo directory to the mount details file in the CASFS root.
	target, err = filepath.Rel(path.Dir(link), target)
	if err != nil {
		return err
	}
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("failed to create symlink from %s to %s: %w", link, target, err)
	}
	return nil
}
