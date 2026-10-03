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

type checkoutCmd struct {
	*gitCmd
	*cmd.ConfigProvider
	*cmd.CachemanProvider

	casgit.CheckoutOptions

	track    string
	noTrack  bool
	detach   bool
	patch    bool
	merge    bool
	force    bool
	quiet    bool
	progress bool
	noProg   bool

	bBranch         string
	bBranchForce    string
	orphan          string
	overlay         bool
	noOverlay       bool
	ignoreSWB       bool
	ignoreOtherWT   bool
	guessRemote     bool
	noGuess         bool
	conflictStyle   string
	pathspecFile    string
	pathspecFileNUL bool
	recurseSub      string
	noRecurseSub    bool
	theirs          bool
	ours            bool
}

func NewCheckoutCmd(gitCmd *gitCmd) *checkoutCmd {
	return &checkoutCmd{gitCmd: gitCmd}
}

func (c *checkoutCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "checkout",
		Usage: "[<options>] [<branch>] [--] [<paths>...]",
		Desc:  `Switch branches or restore working tree files`,
	}
}

func (c *checkoutCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.bBranch, "branch", "b", "", "create and checkout a new branch")
	fl.StringVarP(&c.bBranchForce, "branch-force", "B", "", "create/reset and checkout a branch")
	fl.StringVar(&c.orphan, "orphan", "", "create a new orphan branch")

	fl.BoolVarP(&c.force, "force", "f", false, "force checkout (throw away local modifications)")
	fl.BoolVarP(&c.merge, "merge", "m", false, "perform a 3-way merge with the new branch")
	fl.StringVar(&c.conflictStyle, "conflict", "", "conflict style (merge, diff3, or zdiff3)")
	fl.BoolVarP(&c.patch, "patch", "p", false, "select hunks interactively")
	fl.BoolVarP(&c.quiet, "quiet", "q", false, "suppress progress reporting")
	fl.BoolVar(&c.progress, "progress", false, "force progress reporting")
	fl.BoolVar(&c.noProg, "no-progress", false, "suppress progress reporting")

	fl.BoolVar(&c.detach, "detach", false, "detach HEAD at named commit")

	fl.StringVar(&c.track, "track", "", "set upstream info for new branch")
	fl.Lookup("track").NoOptDefVal = "direct"
	fl.BoolVar(&c.noTrack, "no-track", false, "do not set up tracking")

	fl.BoolVar(&c.theirs, "theirs", false, "checkout their version for unmerged files")
	fl.BoolVar(&c.ours, "ours", false, "checkout our version for unmerged files")

	fl.BoolVar(&c.overlay, "overlay", false, "use overlay mode (default)")
	fl.BoolVar(&c.noOverlay, "no-overlay", false, "remove paths not present in the source tree")

	fl.StringVar(&c.pathspecFile, "pathspec-from-file", "", "read pathspec from file")
	fl.BoolVar(&c.pathspecFileNUL, "pathspec-file-nul", false, "with --pathspec-from-file, pathspec elements are separated with NUL")

	fl.StringVar(&c.recurseSub, "recurse-submodules", "", "control recursive updating of submodules")
	fl.Lookup("recurse-submodules").NoOptDefVal = "on-demand"
	fl.BoolVar(&c.noRecurseSub, "no-recurse-submodules", false, "do not recurse into submodules")

	fl.BoolVar(&c.guessRemote, "guess", true, "second guess 'git checkout <no-such-branch>'")
	fl.BoolVar(&c.noGuess, "no-guess", false, "do not second guess 'git checkout <no-such-branch>'")

	fl.BoolVar(&c.ignoreSWB, "ignore-skip-worktree-bits", false, "do not limit pathspecs to sparse entries only")
	fl.BoolVar(&c.ignoreOtherWT, "ignore-other-worktrees", false, "do not check if another worktree is holding the given ref")
}

func (c *checkoutCmd) checkArgs(fl *pflag.FlagSet) error {
	args := fl.Args()
	dashIdx := -1
	for i, a := range args {
		if a == "--" {
			dashIdx = i
			break
		}
	}
	if dashIdx >= 0 {
		head := args[:dashIdx]
		tail := args[dashIdx+1:]
		switch len(head) {
		case 0:
		case 1:
			c.Target = head[0]
		default:
			return clierr.CLIErr("Too many revisions before '--'")
		}
		c.Paths = tail
	} else {
		switch len(args) {
		case 0:
		case 1:
			c.Target = args[0]
		default:
			c.Target = args[0]
			c.Paths = args[1:]
		}
	}

	c.NewBranch = c.bBranch
	c.NewBranchForce = c.bBranchForce
	c.Orphan = c.orphan
	c.Force = c.force
	c.Merge = c.merge
	c.Quiet = c.quiet
	c.Progress = c.progress
	c.NoProgress = c.noProg
	c.Patch = c.patch
	c.Detach = c.detach
	c.Track = c.track
	c.NoTrack = c.noTrack
	c.Theirs = c.theirs
	c.Ours = c.ours
	c.Overlay = c.overlay
	c.NoOverlay = c.noOverlay
	c.PathspecFromFile = c.pathspecFile
	c.PathspecFileNUL = c.pathspecFileNUL
	c.RecurseSubmodules = c.recurseSub
	c.NoRecurseSubmodules = c.noRecurseSub
	c.GuessRemote = c.guessRemote
	c.NoGuess = c.noGuess
	c.ConflictStyle = c.conflictStyle
	c.IgnoreSkipWorktreeBits = c.ignoreSWB
	c.IgnoreOtherWorktrees = c.ignoreOtherWT
	return nil
}

func (c *checkoutCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	if err := c.checkArgs(fl); err != nil {
		return clierr.CLIUsage(fl, "fatal: %v", err)
	}
	return c.run(ctx)
}

func (c *checkoutCmd) run(ctx context.Context) error {
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}

	gitDir, err := casgit.FindGitDir(pwd)
	if err != nil {
		return err
	}

	client, err := binding.NewClient(gitDir, entities.ABFSSocketFileName, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return err
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("Failed to close client: %v", err)
		}
	}()

	casgitObj := casgit.NewWithClient(client, c.Cacheman)

	if c.GitDir == "" {
		c.GitDir = gitDir
	}
	c.GitDir, err = filepath.Abs(c.GitDir)
	if err != nil {
		return err
	}
	if c.ObjectDir == "" {
		c.ObjectDir = path.Join(c.GitDir, "objects")
	}
	if c.WorkDir == "" {
		c.WorkDir = filepath.Dir(c.GitDir)
	}
	c.WorkDir, err = filepath.Abs(c.WorkDir)
	if err != nil {
		return err
	}

	return casgitObj.Checkout(ctx, c.CheckoutOptions)
}
