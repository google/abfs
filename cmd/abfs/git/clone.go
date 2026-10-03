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

package git

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
)

type cloneCmd struct {
	*gitCmd
	*cmd.ConfigProvider
	*cmd.CachemanProvider

	casgit.CloneOptions
	cloneHash string
	repo      bool
}

func NewCloneCmd(gitCmd *gitCmd) *cloneCmd {
	return &cloneCmd{gitCmd: gitCmd}
}

func (c *cloneCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "clone",
		Usage: "[-o <name>] [-b <name>] [--single-branch] [--no-tags] [--] <repository> [<directory>]",
		Desc:  `Clone a repository into a new directory in a casfs mount`,
	}
}

func (c *cloneCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.Branch, "branch", "b", "", "branch to clone")
	fl.StringVarP(&c.Origin, "origin", "o", "origin", "remote name") //TODO: default to clone.defaultRemoteName from git config
	fl.BoolVarP(&c.repo, "repo", "r", false, "setup in repo manner")
	fl.BoolVarP(&c.SingleBranch, "single-branch", "s", false, "clone only the branch")
	fl.BoolVarP(&c.NoTags, "no-tags", "t", false, "do not clone tags")
	fl.StringVarP(&c.cloneHash, "commit", "c", "", "clone a specific commit (non-standard git option)")
	if err := fl.MarkHidden("commit"); err != nil {
		panic(err)
	}
}

func (c *cloneCmd) checkArgs(fl *pflag.FlagSet) error {
	switch fl.NArg() {
	case 0:
		return clierr.CLIErr("You must specify a repository to clone.")
	case 1:
		c.RemoteRepo = fl.Arg(0)
		c.Directory = filepath.Base(c.RemoteRepo)
	case 2:
		c.RemoteRepo = fl.Arg(0)
		c.Directory = fl.Arg(1)
	default:
		return clierr.CLIErr("Too many arguments.")
	}

	if c.cloneHash != "" {
		if c.Branch != "" {
			return clierr.CLIErr("Cannot specify both --commit and --branch")
		}
		if !entities.IsHash(c.cloneHash) {
			return clierr.CLIErr("Invalid commit hash: %q", c.cloneHash)
		}
		c.Commit = entities.NewHash(c.cloneHash)
	}

	return nil
}

func (c *cloneCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if err := c.checkArgs(fl); err != nil {
		return clierr.CLIUsage(fl, "fatal: %v", err)
	}

	return c.run(ctx)
}

func (c *cloneCmd) run(ctx context.Context) error {
	client, err := binding.NewClient(c.Directory, entities.ABFSSocketFileName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return err
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("error closing client: %v", err)
		}
	}()

	casgit := casgit.NewWithClient(client, c.Cacheman)

	defaultGitDir := path.Join(c.Directory, ".git")
	c.GitDir = os.Getenv("GIT_DIR")
	if c.GitDir == "" {
		c.GitDir = defaultGitDir
	}
	c.GitDir, err = normalizePath(c.GitDir)
	if err != nil {
		return err
	}

	defaultObjectDir := path.Join(c.Directory, ".git", "objects")
	c.ObjectsDir = os.Getenv("GIT_OBJECT_DIRECTORY")
	if c.ObjectsDir == "" {
		c.ObjectsDir = defaultObjectDir
	}
	c.ObjectsDir, err = normalizePath(c.ObjectsDir)
	if err != nil {
		return err
	}

	c.PrefetchIndices = c.Config.GitPrefetchIndices
	c.PrefetchSymlinks = c.Config.GitPrefetchSymlinks
	if err := casgit.Clone(ctx, c.CloneOptions); err != nil {
		return err
	}

	if c.repo {
		if err := c.repoSetup(); err != nil {
			fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
			os.Exit(1)
		}
	}
	return nil
}

// repoSetup is a post-setup mode for `git clone` to enable repo-tools
// compatibility.
// This function should be removed after implementing
// `git init --bare-repository` and `git worktree`.
func (c *cloneCmd) repoSetup() error {
	var err error
	c.Directory, err = normalizePath(c.Directory)
	if err != nil {
		return err
	}

	defaultGitDir := path.Join(c.Directory, ".git")
	defaultObjectDir := path.Join(c.GitDir, "objects")

	symlinks := []struct {
		link   string
		target string
	}{
		{defaultGitDir, c.GitDir},
		{defaultObjectDir, c.ObjectsDir},
	}

	for _, s := range symlinks {
		if s.link != s.target {
			if err := os.MkdirAll(filepath.Dir(s.link), 0755); err != nil {
				return err
			}
			rel, err := filepath.Rel(filepath.Dir(s.link), s.target)
			if err != nil {
				return err
			}
			if err := os.Symlink(rel, s.link); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizePath(p string) (string, error) {
	p = filepath.Clean(p)
	if filepath.IsAbs(p) {
		pwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Rel(pwd, p)
	}
	return p, nil
}
