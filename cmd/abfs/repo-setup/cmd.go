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

package reposetup

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/git"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
)

//go:embed patch
var patchesFS embed.FS

type RepoSetupCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	shgit *git.ShellGit
}

func NewCmd(rootCmd *root.RootCmd) *RepoSetupCmd {
	return &RepoSetupCmd{
		RootCmd: rootCmd,
		shgit:   &git.ShellGit{BinPath: "git"},
	}
}

const PatchedVersion = "fbc9c7919246ea74c295be888db4ae332cced202"

func (c *RepoSetupCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "repo-setup",
		Usage: "<root>",
		Desc:  "Apply ABFS patches to the repo tool",
	}
}

func (c *RepoSetupCmd) RegisterFlags(fl *pflag.FlagSet) {
	// No flags, root is an argument
}

func (c *RepoSetupCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	repoDir, err := os.MkdirTemp("", "repo-setup-*")
	if err != nil {
		log.Fatalf("failed to create temp dir: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Cloning git-repo to %s...\n", repoDir)
	// `abfs push` can't open reftable repos, so force the files ref format in
	// case git defaults to reftable (init.defaultRefFormat).
	cloneCmd := exec.Command("git", "-c", "init.defaultRefFormat=files", "clone", "https://gerrit.googlesource.com/git-repo", repoDir)
	cloneCmd.Stdout = os.Stdout
	cloneCmd.Stderr = os.Stderr
	if err := cloneCmd.Run(); err != nil {
		log.Fatalf("failed to clone git-repo: %v", err)
	}

	entries, err := patchesFS.ReadDir("patch")
	if err != nil {
		log.Fatalf("failed to read patches directory: %v", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return getOrder(entries[i].Name()) < getOrder(entries[j].Name())
	})

	// git am and git rebase create commits, which fails without a committer
	// identity (common on a fresh server). Fall back to a generic one.
	if err := exec.Command("git", "-C", repoDir, "var", "GIT_COMMITTER_IDENT").Run(); err != nil {
		_ = os.Setenv("GIT_COMMITTER_NAME", "ABFS repo-setup")
		_ = os.Setenv("GIT_COMMITTER_EMAIL", "abfs-repo-setup@localhost")
	}

	if err := c.applyAndRebase(repoDir, entries); err != nil {
		return err
	}

	headCmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD")
	headBytes, err := headCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get HEAD hash: %w", err)
	}
	headHash := strings.TrimSpace(string(headBytes))

	fmt.Printf("\nRepository patched successfully in %s\n", repoDir)
	fmt.Printf("To push these changes to the stable-abfs branch, run:\n\n")
	// Push straight to the server: put-ref also goes straight to the server and
	// fails with "missing: <hash>" if the objects are only in the local cache.
	fmt.Printf("  abfs --no-local-cache push -r %s\n", repoDir)
	fmt.Printf("  echo \"%s refs/heads/stable-abfs\" | abfs test-client put-ref -s gerrit.googlesource.com -p git-repo\n\n", headHash)

	return nil
}

func getOrder(name string) int {
	parts := strings.Split(name, "-")
	if len(parts) > 0 {
		if i, err := strconv.Atoi(parts[0]); err == nil {
			return i
		}
	}
	return 0
}
func (c *RepoSetupCmd) applyAndRebase(repoDir string, entries []fs.DirEntry) error {
	// Capture current HEAD to rebase onto it later.
	originalHead, err := c.shgit.RevParse(repoDir, "HEAD")
	if err != nil {
		return fmt.Errorf("failed to get current HEAD: %w", err)
	}

	if err := c.shgit.Checkout(repoDir, PatchedVersion); err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		patchContent, err := patchesFS.ReadFile("patch/" + entry.Name())
		if err != nil {
			log.Fatalf("failed to read patch %s: %v", entry.Name(), err)
		}
		if err := c.am(repoDir, string(patchContent)); err != nil {
			return fmt.Errorf("failed to apply repo patch %s via git am: %w", entry.Name(), err)
		}
	}

	return c.shgit.Rebase(repoDir, originalHead)
}

func (c *RepoSetupCmd) am(repoDir, patch string) error {
	amCmd := exec.Command("git", "am", "--3way")
	amCmd.Dir = repoDir
	amCmd.Stdin = strings.NewReader(patch)
	amCmd.Stdout = os.Stdout
	amCmd.Stderr = os.Stderr
	return amCmd.Run()
}
