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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/google/abfs/shared/entities"
)

type ShellGit struct {
	BinPath string
}

func NewShellGit(binPath string) *ShellGit {
	if binPath == "" {
		binPath = "/usr/bin/git"
	}
	return &ShellGit{BinPath: binPath}
}

func (s *ShellGit) runGitCommand(repoPath string, args ...string) (string, error) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd := exec.Command(s.BinPath, args...)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=safe.bareRepository",
		"GIT_CONFIG_VALUE_0=all",
	)
	cmd.Stderr = stderr
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to run in %s: git %s\nstderr:%s\nerr:%w",
			repoPath, strings.Join(args, " "), stderr.String(), err)
	}
	return stdout.String(), nil
}

// resolveGitDir uses git's built-in `git rev-parse --resolve-git-dir` to validate
// and resolve the git directory for standard working trees, worktrees, and bare repositories.
func (s *ShellGit) resolveGitDir(repoPath string) (string, error) {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		absPath = repoPath
	}

	// 1. Check if <repoPath>/.git is a git directory or a gitfile pointer (working trees, submodules)
	dotGit := filepath.Join(absPath, ".git")
	if out, err := s.runGitCommand("", "rev-parse", "--resolve-git-dir", dotGit); err == nil {
		return strings.TrimSpace(out), nil
	}

	// 2. Check if <repoPath> itself is a bare git directory
	if out, err := s.runGitCommand("", "rev-parse", "--resolve-git-dir", absPath); err == nil {
		return strings.TrimSpace(out), nil
	}

	return "", fmt.Errorf("%s is not a git repository", repoPath)
}

func (s *ShellGit) AddConfig(repoPath string, key, value string) error {
	gitDir, err := s.resolveGitDir(repoPath)
	if err != nil {
		_, err := s.runGitCommand(repoPath, "config", "set", "--local", key, value)
		return err
	}
	_, err = s.runGitCommand(repoPath, "--git-dir="+gitDir, "config", key, value)
	return err
}

func (s *ShellGit) GetConfig(repoPath string, key, defValue string) (string, error) {
	gitDir, err := s.resolveGitDir(repoPath)
	if err != nil {
		val, err := s.runGitCommand(repoPath, "config", "get", "--local", "--default", defValue, key)
		return strings.TrimSpace(val), err
	}
	val, err := s.runGitCommand(repoPath, "--git-dir="+gitDir, "config", "--default", defValue, key)
	return strings.TrimSpace(val), err
}

func (s *ShellGit) GetAllConfigs(repoPath, key string) ([]string, error) {
	gitDir, err := s.resolveGitDir(repoPath)
	var val string
	if err == nil {
		val, err = s.runGitCommand(repoPath, "--git-dir="+gitDir, "config", "--get-all", key)
	} else {
		val, err = s.runGitCommand(repoPath, "config", "--get-all", "--local", key)
	}
	if err != nil {
		return nil, err
	}
	var res []string
	for _, v := range strings.Split(val, "\n") {
		v = strings.TrimSpace(v)
		if v != "" {
			res = append(res, v)
		}
	}
	return res, nil
}

// Fetch shells out to `git fetch` because it can be significantly faster than go-git's Fetch
// for large network downloads.
func (s *ShellGit) Fetch(repoPath string) error {
	_, err := s.runGitCommand(repoPath, "fetch", "--force", "--prune", "--all")
	return err
}

func (s *ShellGit) FetchRef(repoPath string, remote string, ref string) error {
	_, err := s.runGitCommand(repoPath, "fetch", remote, ref)
	return err
}

func (s *ShellGit) SymRef(repoPath string, ref string) (string, error) {
	out, err := s.runGitCommand(repoPath, "symbolic-ref", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (s *ShellGit) ListSymRefs(remote string) ([]string, []string, error) {
	out, err := s.runGitCommand("", "ls-remote", "--symref", remote)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	targets := make([]string, 0, len(lines))
	refs := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.HasPrefix(line, "ref: ") {
			break
		}
		line = strings.TrimPrefix(line, "ref: ")

		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			return nil, nil, fmt.Errorf("error parsing `git ls-remote` output - this line had an unexpected number of tokens %q", line)
		}
		targets = append(targets, parts[0])
		refs = append(refs, parts[1])
	}
	return targets, refs, nil
}

// FetchAndGetUpdatedRefs returns the refs that were updated by parsing `git fetch --porcelain` output
func (s *ShellGit) FetchAndGetUpdatedRefs(repoPath string) ([]*FetchedRef, error) {
	stdout, close, err := s.streamingGitCommand(repoPath, "fetch", "--prune", "--porcelain")
	if err != nil {
		return nil, err
	}
	fetchedRefs, err := parseFetchPorcelainOutput(stdout)
	if err != nil {
		if err := close(); err != nil {
			log.Printf("Error while closing git fetch command: %s", err)
		}
		return nil, err
	}
	if err := close(); err != nil {
		return nil, err
	}

	return fetchedRefs, nil
}

// parseFetchPorcelainOutput expects the format:
// <flag> <old-object-id> <new-object-id> <local-reference>
// e.g. * 63ca92cd386869f82656153fe0a9c6df42a61ab4 d45dfce60bc7aea72d3a7b329d8b1de40d48e831 refs/tags/mirror-upstream
// The tricky part is that the <flag> can be a space and the delimiter is a space.
// See https://git-scm.com/docs/git-fetch#_output
func parseFetchPorcelainOutput(input io.Reader) ([]*FetchedRef, error) {
	scanner := bufio.NewScanner(input)
	refs := make([]*FetchedRef, 0)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 2 {
			return nil, fmt.Errorf("error parsing `git fetch` output - this line had length < 2 %q", line)
		}
		ref := &FetchedRef{StatusFlag: line[:1]}
		tokens := strings.Split(line[2:], " ")
		if len(tokens) != 3 {
			return nil, fmt.Errorf("error parsing `git fetch` output - this line had an unexpected number of tokens %q", line)
		}
		ref.OldObjectId = plumbing.NewHash(tokens[0])
		ref.NewObjectId = plumbing.NewHash(tokens[1])
		ref.LocalReference = tokens[2]
		refs = append(refs, ref)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("`git fetch` output scanning returned an error: %w", err)
	}
	return refs, nil
}

type FetchedRef struct {
	// we can parse the flag into a type if we need to use it
	StatusFlag     string
	OldObjectId    plumbing.Hash
	NewObjectId    plumbing.Hash
	LocalReference string
}

// GetRemotes is useful when a negative refspec is used in a remote. go-git doesn't support
// negative refspecs so it fails to parse remotes using them.
func (s *ShellGit) GetRemotes(repoPath string) ([]string, error) {
	out, err := s.runGitCommand(repoPath, "remote")
	if err != nil {
		return nil, err
	}

	return strings.Split(out, "\n"), nil
}

// AddFetchRefSpec is useful to use with negative refspecs since go-git doesn't support them.
// https://github.com/go-git/go-git/issues/538.
func (s *ShellGit) AddFetchRefSpec(repoPath, remoteName, refSpec string) error {
	// will likely need to pass --porcelain instead of --quiet then parse the output to see what was fetched
	_, err := s.runGitCommand(repoPath, "config", "--add", fmt.Sprintf("remote.%s.fetch", remoteName), refSpec)
	return err
}

// AddRemote is here because go-git doesn't support negative refspecs and will fail to add
// a remote if the config already contains them.
func (s *ShellGit) AddRemote(repoPath, remoteName, url string) error {
	_, err := s.runGitCommand(repoPath, "remote", "add", "--mirror", remoteName, url)
	return err
}

func (s *ShellGit) RevParse(repoPath string, ref string) (string, error) {
	out, err := s.runGitCommand(repoPath, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (s *ShellGit) Checkout(repoPath string, ref string) error {
	_, err := s.runGitCommand(repoPath, "checkout", ref)
	return err
}

func (s *ShellGit) Rebase(repoPath string, onto string) error {
	_, err := s.runGitCommand(repoPath, "rebase", onto)
	return err
}

func (s *ShellGit) RebaseOnto(repoPath string, newbase, oldbase, tip string) error {
	_, err := s.runGitCommand(repoPath, "rebase", "--onto", newbase, oldbase, tip)
	return err
}

func (s *ShellGit) ResetSoft(repoPath string, target string) error {
	_, err := s.runGitCommand(repoPath, "reset", "--soft", target)
	return err
}

func (s *ShellGit) UnsetUpstream(repoPath string, branch string) error {
	args := []string{"branch", "--unset-upstream"}
	if branch != "" {
		args = append(args, branch)
	}
	_, err := s.runGitCommand(repoPath, args...)
	return err
}

func (s *ShellGit) SymbolicRef(repoPath string, ref string, short bool) (string, error) {
	args := []string{"symbolic-ref"}
	if short {
		args = append(args, "--short")
	}
	args = append(args, ref)
	out, err := s.runGitCommand(repoPath, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (s *ShellGit) CheckoutNewBranch(repoPath, branch, tracking string) error {
	_, err := s.runGitCommand(repoPath, "checkout", "-b", branch, tracking)
	return err
}

func (s *ShellGit) RenameRemote(repoPath, old, new string) error {
	_, err := s.runGitCommand(repoPath, "remote", "rename", old, new)
	return err
}

func (s *ShellGit) RemoveRemote(repoPath, remoteName string) error {
	_, err := s.runGitCommand(repoPath, "remote", "rm", remoteName)
	return err
}

func (s *ShellGit) TryRemoveRemote(repoPath, remoteName string) error {
	_, err := s.runGitCommand(repoPath, "remote", "rm", remoteName)
	if err != nil {
		if strings.Contains(err.Error(), "No such remote") {
			return nil
		}
	}

	return err
}

func (s *ShellGit) ShowRefs(repoPath string) (map[string]entities.Hash, error) {
	out, err := s.runGitCommand(repoPath, "show-ref")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(out, "\n")
	result := make(map[string]entities.Hash, len(lines))
	for _, line := range lines {
		if len(strings.TrimSpace(line)) == 0 {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("unexpected number of fields in %q", line)
		}
		hash := entities.NewHash(fields[0])
		result[fields[1]] = hash
	}

	return result, nil
}

// streamingGitCommand starts the provided command in the provided directory and returns a
// reader for stdout and a close function that waits for the command to be exit.
func (s *ShellGit) streamingGitCommand(repoPath string, cmdArgs ...string) (io.Reader, func() error, error) {
	return s.streamingGitCommandStdin(repoPath, "", cmdArgs...)
}

// streamingGitCommand starts the provided command in the provided directory and returns a
// reader for stdout and a close function that waits for the command to be exit.
func (s *ShellGit) streamingGitCommandStdin(repoPath string, stdin string, cmdArgs ...string) (io.Reader, func() error, error) {
	stderr := new(bytes.Buffer)
	cmd := exec.Command(s.BinPath, cmdArgs...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Dir = repoPath
	cmd.Stderr = stderr
	// even though stdout is io.ReadCloser, the docs say it's automatically closed when we Wait the command
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to retrieve stdout pipe from the `git %s` command. err: %w, dir: %q, cmd: %q", cmdArgs[0], err, cmd.Dir, cmd.String())
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("failed to start `git %s`. stderr: %q, err: %w, dir: %q, cmd: %q", cmdArgs[0], stderr.String(), err, cmd.Dir, cmd.String())
	}

	closeFunc := func() error {
		_, _ = io.Copy(io.Discard, stdout) // drain stdout
		err := cmd.Wait()
		runtime := time.Since(start)
		if runtime > time.Minute {
			log.Printf("warning: shellgit's `git %s` took %s. stderr: %q, err: %v, dir: %q, cmd: %q", cmdArgs[0], runtime, stderr.String(), err, cmd.Dir, cmd.String())
		}
		if err != nil {
			return fmt.Errorf("error waiting on `git %s`. stderr: %q, err: %w, dir: %q, cmd: %q", cmdArgs[0], stderr.String(), err, cmd.Dir, cmd.String())
		}
		return nil
	}

	return stdout, closeFunc, nil
}

func excludesForCommand(excludes []entities.Hash) []string {
	s := make([]string, len(excludes))
	for i, e := range excludes {
		s[i] = e.String()
	}
	return s
}

// GetObjCount returns the number of objects reachable from rev but unreachable
// from the exclude set.
func (s *ShellGit) GetObjCount(repoPath, rev string, excludes []entities.Hash) (int64, error) {
	args := []string{
		"rev-list",
		"--objects",
		"--count",
		rev,
	}
	if len(excludes) > 0 {
		args = append(args, "--not")
		args = append(args, excludesForCommand(excludes)...)
	}
	stdout, err := s.runGitCommand(repoPath, args...)
	if err != nil {
		return 0, err
	}
	trimmed := strings.TrimSpace(stdout)
	cnt, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, err
	}

	return cnt, nil
}

// batchCheckObjectInfoRaw outputs object type and name information of objects
// read from reader in raw bytes to the writer.
func (s *ShellGit) batchCheckObjectInfoRaw(repoPath string, reader io.Reader, writer io.Writer) error {
	stderr := new(bytes.Buffer)
	args := []string{
		"cat-file",
		"--batch-check=%(objecttype) %(objectname)",
	}

	cmd := exec.Command(s.BinPath, args...)
	cmd.Dir = repoPath
	cmd.Stdin = reader
	cmd.Stdout = writer
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to batch-check objects in directory %q.\nstderr:%s\nerr: %w", repoPath, stderr.String(), err)
	}

	return nil
}

func (s *ShellGit) ObjectsInTree(repoPath, ref string) ([]*ObjectTypeName, error) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	args := []string{
		"ls-tree",
		ref,
		"-rt",
		"--format=%(objecttype) %(objectname)",
	}
	cmd := exec.Command(s.BinPath, args...)
	cmd.Dir = repoPath
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to ls-tree in directory %q.\nstderr:%s\nerr: %w", repoPath, stderr.String(), err)
	}

	uniq := make(map[string]bool)
	scanner := bufio.NewScanner(stdout)

	var objs []*ObjectTypeName

	for scanner.Scan() {
		split := strings.Split(scanner.Text(), " ")
		if len(split) != 2 {
			return nil, fmt.Errorf("error parsing `git ls-tree` output - this line had an unexpected number of tokens %q", scanner.Text())
		}
		if uniq[split[1]] {
			continue
		}
		uniq[split[1]] = true

		objType, err := plumbing.ParseObjectType(split[0])
		if err != nil {
			return nil, err
		}
		// Skip git submodules to align with behavior of `git rev-list --objects`.
		if objType == plumbing.CommitObject {
			continue
		}
		if !plumbing.IsHash(split[1]) {
			return nil, fmt.Errorf("%q is an invalid hash", split[1])
		}
		name := plumbing.NewHash(split[1])
		objs = append(objs, &ObjectTypeName{ObjType: objType, Name: entities.Hash(name)})
	}
	if scanner.Err() != nil {
		return nil, scanner.Err()
	}

	return objs, nil
}

func (s *ShellGit) PackObjects(repoPath string, prefix string, objects []entities.Hash) (string, error) {
	stdin := new(bytes.Buffer)
	for _, o := range objects {
		stdin.WriteString(o.String())
		stdin.WriteString("\n")
	}

	cmd := exec.Command(s.BinPath, "pack-objects", prefix)
	cmd.Stdin = stdin
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		var exErr *exec.ExitError
		if errors.As(err, &exErr) {
			return "", fmt.Errorf("git pack-objects: %s: %w", exErr.Stderr, err)
		}
		return "", err
	}

	return prefix + "-" + strings.TrimSpace(string(out)), nil
}

// Maps a path to the git lfs remote OID
func (s *ShellGit) lfsFiles(repoPath string, all bool) (map[string][]string, error) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	// output format looks like
	// 6e1b890dafa9956b1bdff43b3fd46aaf273085a1d2041bde7efce6cf5eb0262b * LargeFile.zip
	args := []string{"lfs", "ls-files", "--long"}
	if all {
		args = append(args, "--all")
	}
	cmd := exec.Command(s.BinPath, args...)
	cmd.Dir = repoPath
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		var exErr *exec.ExitError
		if errors.As(err, &exErr) {
			return nil, fmt.Errorf("lfs ls-files failed with err: %w. stderr: %v", err, stderr.String())
		}
		return nil, err
	}

	scanner := bufio.NewScanner(stdout)
	files := make(map[string][]string)
	for scanner.Scan() {
		splits := strings.Split(scanner.Text(), " ")
		if len(splits) != 3 {
			return nil, fmt.Errorf("expected 3 splits for line: %q", scanner.Text())
		}
		files[splits[2]] = append(files[splits[2]], splits[0])
		if !all {
			if len(files[splits[2]]) > 1 {
				return nil, fmt.Errorf("file %q has more than 1 OID for a single ref", splits[2])
			}
		}
	}
	if scanner.Err() != nil {
		return nil, scanner.Err()
	}

	return files, nil
}

// LFS files for the entire history.
func (s *ShellGit) LFSFilesAll(repoPath string) (map[string][]string, error) {
	return s.lfsFiles(repoPath, true)
}

// LFS files at HEAD.
func (s *ShellGit) LFSFiles(repoPath string) (map[string][]string, error) {
	return s.lfsFiles(repoPath, false)
}

func (s *ShellGit) LFSFetch(repoPath string) error {
	stderr := new(bytes.Buffer)

	cmd := exec.Command(s.BinPath, "lfs", "fetch", "--all")
	cmd.Dir = repoPath
	cmd.Stderr = stderr
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("lfs fetch failed with err: %w. stderr: %v", err, stderr.String())
	}

	return nil
}

func (s *ShellGit) LFSEnabled() bool {
	return exec.Command(s.BinPath, "lfs").Run() == nil
}

func (s *ShellGit) UpdateIndex(repoPath, file string) error {
	cmd := exec.Command(s.BinPath, "update-index", file)
	cmd.Dir = repoPath
	return cmd.Run()
}

func (s *ShellGit) InstallLFSHooks(repoPath string) error {
	cmd := exec.Command(s.BinPath, "lfs", "update")
	cmd.Dir = repoPath
	return cmd.Run()
}

func (s *ShellGit) GitCloneBranchAt(ctx context.Context, repoPath, targetURL, branch string) error {
	cmd := exec.CommandContext(ctx, s.BinPath, "clone", targetURL, "-b", branch, ".")
	cmd.Dir = repoPath
	return cmd.Run()
}

// GitCloneCommitAt clones a specific commit/tag/branch from a remote repository into the given path.
func (s *ShellGit) GitCloneCommitAt(repoPath, targetURL, remoteName, ref string) error {
	_, err := s.runGitCommand(repoPath, "init")
	if err != nil {
		return fmt.Errorf("failed to initialize git repository: %v", err)
	}

	_, err = s.runGitCommand(repoPath, "remote", "add", remoteName, targetURL)
	if err != nil {
		return fmt.Errorf("failed to add remote: %v", err)
	}

	_, err = s.runGitCommand(repoPath, "fetch", remoteName, ref)
	if err != nil {
		return fmt.Errorf("failed to fetch ref: %v", err)
	}

	_, err = s.runGitCommand(repoPath, "checkout", "FETCH_HEAD")
	if err != nil {
		return fmt.Errorf("failed to checkout fetched head: %v", err)
	}

	return nil
}

// GitCloneBareWithWorktree initializes a bare git repository at gitDir configured for workTree,
// fetches the specified ref from targetURL, and checks out a detached HEAD into workTree.
func (s *ShellGit) GitCloneBareWithWorktree(ctx context.Context, gitDir, workTree, targetURL, remoteName, ref string) error {
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		return fmt.Errorf("failed to create git dir %s: %w", gitDir, err)
	}
	if err := os.MkdirAll(workTree, 0755); err != nil {
		return fmt.Errorf("failed to create worktree %s: %w", workTree, err)
	}

	gitDirAbs, err := filepath.Abs(gitDir)
	if err != nil {
		gitDirAbs = gitDir
	}
	workTreeAbs, err := filepath.Abs(workTree)
	if err != nil {
		workTreeAbs = workTree
	}

	cmd := exec.CommandContext(ctx, s.BinPath, "init", "--bare", "--ref-format=files", gitDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		cmd = exec.CommandContext(ctx, s.BinPath, "init", "--bare", gitDir)
		if out, err = cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to git init bare in %s: %s: %w", gitDir, string(out), err)
		}
	}

	cmd = exec.CommandContext(ctx, s.BinPath, "--git-dir="+gitDir, "remote", "add", remoteName, targetURL)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add remote %s: %s: %w", remoteName, string(out), err)
	}

	if err := s.AddConfig(gitDir, "core.bare", "false"); err != nil {
		return fmt.Errorf("failed to set core.bare: %w", err)
	}
	if err := s.AddConfig(gitDir, "core.worktree", workTreeAbs); err != nil {
		return fmt.Errorf("failed to set core.worktree: %w", err)
	}

	fetchArg := ref
	if fetchArg == "" || fetchArg == "HEAD" {
		fetchArg = "HEAD"
	}

	cmd = exec.CommandContext(ctx, s.BinPath, "--git-dir="+gitDir, "fetch", remoteName, fetchArg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to fetch %s: %s: %w", fetchArg, string(out), err)
	}

	cmd = exec.CommandContext(ctx, s.BinPath, "--git-dir="+gitDir, "--work-tree="+workTree, "checkout", "-f", "--detach", "FETCH_HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to checkout worktree %s: %s: %w", workTree, string(out), err)
	}

	dotGit := filepath.Join(workTree, ".git")
	if _, err := os.Lstat(dotGit); os.IsNotExist(err) {
		rel, err := filepath.Rel(workTree, gitDir)
		if err == nil {
			if symlinkErr := os.Symlink(rel, dotGit); symlinkErr == nil {
				return nil
			}
		}
		if err := os.WriteFile(dotGit, []byte("gitdir: "+gitDirAbs+"\n"), 0644); err != nil {
			return fmt.Errorf("failed to create .git in worktree %s: %w", workTree, err)
		}
	}

	return nil
}

func (s *ShellGit) AllObjects(repoPath string) (objects []entities.Hash, returnErr error) {
	reader, close, err := s.streamingGitCommand(repoPath, "cat-file", "--batch-check=%(objectname)", "--batch-all-objects", "--unordered")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()

	scanner := bufio.NewScanner(reader)
	scanner.Split(bufio.ScanLines)
	hashes := make([]entities.Hash, 0)
	for scanner.Scan() {
		hashes = append(hashes, entities.NewHash(scanner.Text()))
	}
	return hashes, scanner.Err()
}

func (s *ShellGit) AllCommitsDates(repoPath string) (hashes []entities.Hash, dates []time.Time, returnErr error) {
	reader, close, err := s.streamingGitCommand(repoPath, "log", "--all", "--pretty=format:%H %cd", "--date=iso-strict")
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err := close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()

	scanner := bufio.NewScanner(reader)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return nil, nil, fmt.Errorf("invalid log line %q: expected 2 parts, got %d", line, len(parts))
		}
		hashHex, dateStr := parts[0], parts[1]
		if !entities.IsHash(hashHex) {
			return nil, nil, fmt.Errorf("invalid hash %q", hashHex)
		}
		hashes = append(hashes, entities.NewHash(hashHex))
		date, err := time.Parse(time.RFC3339, dateStr)
		if err != nil {
			dropTZ := strings.Split(dateStr, "+")[0] + "Z"
			var err2 error
			date, err2 = time.Parse(time.RFC3339, dropTZ)
			if err2 != nil {
				return nil, nil, fmt.Errorf("invalid date %q: %w", dateStr, err)
			}
		}
		dates = append(dates, date)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return hashes, dates, nil
}

func (s *ShellGit) ObjectTypes(repoPath string, objects []entities.Hash) (types []string, returnErr error) {
	var sb strings.Builder
	for _, object := range objects {
		fmt.Fprintf(&sb, "%s\n", object.String())
	}

	reader, close, err := s.streamingGitCommandStdin(repoPath, sb.String(), "cat-file", "--batch-check=%(objecttype)")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()

	types = make([]string, 0, len(objects))
	scanner := bufio.NewScanner(reader)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		types = append(types, strings.TrimSpace(scanner.Text()))
	}
	return types, scanner.Err()
}

func (s *ShellGit) CommitsFileObjects(repoPath string, commits []entities.Hash) (hashes []entities.Hash, returnErr error) {
	var sb strings.Builder
	for _, commit := range commits {
		fmt.Fprintf(&sb, "%s\n", commit.String())
	}

	args := []string{
		"rev-list",
		"--objects",
		"--stdin",
	}
	reader, close, err := s.streamingGitCommandStdin(repoPath, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()

	scanner := bufio.NewScanner(reader)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), " ", 2)
		hashes = append(hashes, entities.NewHash(fields[0]))
	}
	return hashes, scanner.Err()
}

func (s *ShellGit) CommitTreeHashes(repoPath string, commits []entities.Hash) ([]entities.Hash, error) {
	hashes := make([]entities.Hash, 0, len(commits))
	for chunk := range slices.Chunk(commits, 10000) {
		chunkHashes, err := s.commitTreeHashes(repoPath, chunk)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, chunkHashes...)
	}
	return hashes, nil
}

func (s *ShellGit) commitTreeHashes(repoPath string, commits []entities.Hash) (hashes []entities.Hash, returnErr error) {
	args := make([]string, 0, len(commits)+1)
	args = append(args, "rev-parse")
	for _, commit := range commits {
		args = append(args, commit.String()+"^{tree}")
	}

	reader, close, err := s.streamingGitCommand(repoPath, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()

	hashes = make([]entities.Hash, 0, len(commits))
	scanner := bufio.NewScanner(reader)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		hex := strings.TrimSpace(scanner.Text())
		if !entities.IsHash(hex) {
			return nil, fmt.Errorf("invalid hash: %s", hex)
		}
		hashes = append(hashes, entities.NewHash(hex))
	}
	return hashes, scanner.Err()
}

func (s *ShellGit) CurrentBranch(repoPath string) (branch, remote string, err error) {
	branch, err = s.SymbolicRef(repoPath, "HEAD", true)
	if err != nil {
		return
	}
	if branch == "" {
		return
	}
	remote, err = s.GetConfig(repoPath, fmt.Sprintf("branch.%s.remote", branch), "")
	return
}

func splitNUL(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}

	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}

	return 0, nil, nil
}

func parseLongListTree(line string) (entities.DeepEntry, error) {
	if line == "" {
		return entities.DeepEntry{}, fmt.Errorf("empty line")
	}
	parts := strings.SplitN(line, "\t", 2)
	if len(parts) != 2 {
		return entities.DeepEntry{}, fmt.Errorf("invalid ls-tree line: %q", line)
	}
	meta, name := parts[0], parts[1]

	metaParts := strings.Fields(meta)
	if len(metaParts) != 4 {
		return entities.DeepEntry{}, fmt.Errorf("invalid metadata in ls-tree line: %q", meta)
	}
	modeStr, _, hashStr, sizeStr := metaParts[0], metaParts[1], metaParts[2], metaParts[3]

	mode, err := entities.FileModeNew(modeStr)
	if err != nil {
		return entities.DeepEntry{}, fmt.Errorf("invalid mode %q: %w", modeStr, err)
	}

	if !entities.IsHash(hashStr) {
		return entities.DeepEntry{}, fmt.Errorf("invalid hash %q", hashStr)
	}
	hash := entities.NewHash(hashStr)

	var size int64
	if sizeStr != "-" {
		var err error
		size, err = strconv.ParseInt(sizeStr, 10, 64)
		if err != nil {
			return entities.DeepEntry{}, fmt.Errorf("invalid size %q: %w", sizeStr, err)
		}
	}

	return entities.DeepEntry{
		Name: name,
		Mode: mode,
		Hash: hash,
		Size: size,
	}, nil
}

func (s *ShellGit) ListTreeRec(repoPath, ref string) ([]entities.DeepEntry, error) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	args := []string{
		"ls-tree",
		ref,
		"-zrtl",
	}
	cmd := exec.Command(s.BinPath, args...)
	cmd.Dir = repoPath
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to ls-tree in directory %q.\nstderr:%s\nerr: %w", repoPath, stderr.String(), err)
	}

	var entries []entities.DeepEntry

	scanner := bufio.NewScanner(stdout)
	scanner.Split(splitNUL)
	for scanner.Scan() {
		ent, err := parseLongListTree(scanner.Text())
		if err != nil {
			return nil, err
		}
		entries = append(entries, ent)
	}
	if scanner.Err() != nil {
		return nil, scanner.Err()
	}

	return entries, nil
}

func (s *ShellGit) HasUncommitedChanges(repoPath string) (bool, error) {
	out, err := s.runGitCommand(repoPath, "status", "--porcelain", "-uno")
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(out)) > 0, nil
}

func (s *ShellGit) StashPush(repoPath string) error {
	_, err := s.runGitCommand(repoPath, "stash", "push")
	return err
}

func (s *ShellGit) StashPop(repoPath string) error {
	_, err := s.runGitCommand(repoPath, "stash", "pop")
	return err
}

func (s *ShellGit) RevListReverse(repoPath, from, to string) ([]string, error) {
	out, err := s.runGitCommand(repoPath, "rev-list", "--reverse", fmt.Sprintf("%s..%s", from, to))
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (s *ShellGit) UpdateRef(repoPath, refName, commitHash string) error {
	_, err := s.runGitCommand(repoPath, "update-ref", refName, commitHash)
	return err
}

func (s *ShellGit) SetSymbolicRef(repoPath, name, ref string) error {
	_, err := s.runGitCommand(repoPath, "symbolic-ref", name, ref)
	return err
}
