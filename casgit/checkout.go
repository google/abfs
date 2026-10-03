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

package casgit

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/filesystem/dirsync"
	"github.com/google/abfs/maps"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
)

type CheckoutOptions struct {
	Target string
	Paths  []string

	NewBranch      string
	NewBranchForce string
	Orphan         string

	Track       string
	NoTrack     bool
	GuessRemote bool
	NoGuess     bool

	Detach bool

	Force         bool
	Merge         bool
	ConflictStyle string
	Quiet         bool
	Progress      bool
	NoProgress    bool

	Theirs bool
	Ours   bool
	Patch  bool

	Overlay   bool
	NoOverlay bool

	PathspecFromFile string
	PathspecFileNUL  bool

	RecurseSubmodules      string
	NoRecurseSubmodules    bool
	IgnoreSkipWorktreeBits bool
	IgnoreOtherWorktrees   bool

	GitDir    string
	ObjectDir string
	WorkDir   string
}

type GitCheckout struct {
	CheckoutOptions
	*GitRepo

	absWorkTree string

	targetCommit entities.Hash
	targetTree   entities.Hash

	branchToCreate string
	createForce    bool
	orphanMode     bool

	headRef  string
	detached bool

	trackRef    string
	trackRemote string
}

func (g *CASGit) Checkout(ctx context.Context, opts CheckoutOptions) error {
	gitRepo := NewGitRepo(g, opts.WorkDir, opts.GitDir, opts.ObjectDir)
	if err := gitRepo.Load(); err != nil {
		return fmt.Errorf("failed to load git repo: %w", err)
	}
	c := &GitCheckout{
		CheckoutOptions: opts,
		GitRepo:         gitRepo,
	}
	return c.Checkout(ctx)
}

func (c *GitCheckout) Checkout(ctx context.Context) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := c.setupPaths(); err != nil {
		return err
	}

	if len(c.Paths) > 0 || c.PathspecFromFile != "" {
		return c.checkoutPaths(ctx)
	}
	return c.switchTarget(ctx)
}

func (c *GitCheckout) validate() error {
	if c.Patch {
		return fmt.Errorf("--patch is not supported")
	}
	if c.Merge {
		return fmt.Errorf("--merge is not supported")
	}
	if c.Theirs || c.Ours {
		return fmt.Errorf("--ours/--theirs require merge support which is not implemented")
	}
	if c.ConflictStyle != "" {
		return fmt.Errorf("--conflict is not supported")
	}
	if c.RecurseSubmodules != "" && !c.NoRecurseSubmodules {
		return fmt.Errorf("--recurse-submodules is not supported")
	}

	if _, err := os.Stat(path.Join(c.GitDir, "MERGE_HEAD")); err == nil {
		return fmt.Errorf("you are in the middle of a merge -- cannot checkout")
	}
	if _, err := os.Stat(path.Join(c.GitDir, "CHERRY_PICK_HEAD")); err == nil {
		return fmt.Errorf("you are in the middle of a cherry-pick -- cannot checkout")
	}

	cnt := 0
	if c.NewBranch != "" {
		cnt++
	}
	if c.NewBranchForce != "" {
		cnt++
	}
	if c.Orphan != "" {
		cnt++
	}
	if cnt > 1 {
		return fmt.Errorf("-b, -B and --orphan are mutually exclusive")
	}
	if c.Detach && (c.NewBranch != "" || c.NewBranchForce != "" || c.Orphan != "") {
		return fmt.Errorf("--detach cannot be used with -b/-B/--orphan")
	}
	if c.Track != "" && c.NoTrack {
		return fmt.Errorf("--track and --no-track are mutually exclusive")
	}
	if c.PathspecFileNUL && c.PathspecFromFile == "" {
		return fmt.Errorf("--pathspec-file-nul requires --pathspec-from-file")
	}

	if c.NewBranch != "" {
		c.branchToCreate = c.NewBranch
	}
	if c.NewBranchForce != "" {
		c.branchToCreate = c.NewBranchForce
		c.createForce = true
	}
	if c.Orphan != "" {
		c.branchToCreate = c.Orphan
		c.orphanMode = true
	}
	return nil
}

func (c *GitCheckout) setupPaths() error {
	if c.GitDir == "" {
		return fmt.Errorf("git dir not set")
	}
	if c.WorkDir == "" {
		c.WorkDir = filepath.Dir(c.GitDir)
	}
	abs, err := filepath.Abs(c.WorkDir)
	if err != nil {
		return err
	}
	c.absWorkTree = abs

	if c.PathspecFromFile != "" {
		extra, err := readPathspecFile(c.PathspecFromFile, c.PathspecFileNUL)
		if err != nil {
			return err
		}
		c.Paths = append(c.Paths, extra...)
	}
	return nil
}

func readPathspecFile(p string, nul bool) ([]string, error) {
	var data []byte
	var err error
	if p == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(p)
	}
	if err != nil {
		return nil, err
	}
	sep := byte('\n')
	if nul {
		sep = 0
	}
	parts := strings.Split(string(data), string(sep))
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		s = strings.TrimRight(s, "\r")
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c *GitCheckout) switchTarget(ctx context.Context) error {
	target := c.Target
	createOnly := c.branchToCreate != "" && target == ""
	if createOnly {
		target = "HEAD"
	}

	if c.orphanMode {
		return c.checkoutOrphan(ctx, target)
	}

	commit, refName, isBranch, err := c.resolveTarget(ctx, target)
	if err != nil {
		return err
	}
	c.targetCommit = entities.Hash(commit.Hash)
	c.targetTree = entities.Hash(commit.TreeHash)

	switch {
	case c.branchToCreate != "":
		c.headRef = "refs/heads/" + c.branchToCreate
		c.detached = false
	case c.Detach || !isBranch:
		c.detached = true
	default:
		c.headRef = refName
		c.detached = false
	}

	if c.branchToCreate != "" {
		if err := c.createBranch(); err != nil {
			return err
		}
		if err := c.setupTracking(refName, isBranch); err != nil {
			return err
		}
	}

	if err := c.applyTreeToWorkdir(ctx, c.targetTree); err != nil {
		return err
	}

	if err := c.casgit.client.LoadTreeDigestMap(ctx, c.targetTree); err != nil {
		log.Printf("cannot load digest map for checkout (tree=%s): %v", c.targetTree, err)
	}

	if err := c.writeIndex(ctx, c.targetTree); err != nil {
		return err
	}
	if err := c.updateHEAD(); err != nil {
		return err
	}
	if err := c.writeOrigHead(); err != nil {
		return err
	}

	if !c.Quiet {
		c.printSwitchMessage(refName, isBranch)
	}
	return nil
}

func (c *GitCheckout) checkoutOrphan(ctx context.Context, target string) error {
	commit, _, _, err := c.resolveTarget(ctx, target)
	var treeHash entities.Hash
	if err == nil {
		treeHash = entities.Hash(commit.TreeHash)
	} else if target != "" && target != "HEAD" {
		return err
	}

	c.headRef = "refs/heads/" + c.branchToCreate
	c.detached = false

	if !treeHash.IsZero() {
		if err := c.applyTreeToWorkdir(ctx, treeHash); err != nil {
			return err
		}
		if err := c.casgit.client.LoadTreeDigestMap(ctx, treeHash); err != nil {
			log.Printf("cannot load digest map for orphan checkout (tree=%s): %v", treeHash, err)
		}
		if err := c.writeIndex(ctx, treeHash); err != nil {
			return err
		}
	} else {
		_ = os.Remove(path.Join(c.GitDir, "index"))
	}

	if err := WriteFileAtomic(path.Join(c.GitDir, "HEAD"), []byte("ref: "+c.headRef+"\n"), 0644); err != nil {
		return err
	}
	if err := c.writeOrigHead(); err != nil {
		return err
	}
	if !c.Quiet {
		fmt.Printf("Switched to a new branch '%s'\n", c.branchToCreate)
	}
	return nil
}

func (c *GitCheckout) createBranch() error {
	refPath := path.Join(c.GitDir, "refs/heads/", c.branchToCreate)
	if !c.createForce {
		if _, err := os.Stat(refPath); err == nil {
			return fmt.Errorf("a branch named '%s' already exists", c.branchToCreate)
		} else if !entities.IsNotExist(err) {
			return err
		}
		refs, err := c.casgit.readPackedRefs(c.GitDir)
		if err != nil {
			return err
		}
		if _, ok := refs["refs/heads/"+c.branchToCreate]; ok {
			return fmt.Errorf("a branch named '%s' already exists", c.branchToCreate)
		}
	}
	if err := os.MkdirAll(filepath.Dir(refPath), 0755); err != nil {
		return err
	}
	return WriteFileAtomic(refPath, []byte(c.targetCommit.String()+"\n"), 0644)
}

func (c *GitCheckout) setupTracking(sourceRef string, isBranch bool) error {
	if c.NoTrack {
		return nil
	}
	track := c.Track
	if track == "" && !c.NoTrack {
		if strings.HasPrefix(sourceRef, "refs/remotes/") {
			track = "direct"
		}
	}
	if track == "" || track == "false" {
		return nil
	}
	if !isBranch && !strings.HasPrefix(sourceRef, "refs/remotes/") {
		return nil
	}

	var remote, mergeRef string
	switch {
	case strings.HasPrefix(sourceRef, "refs/remotes/"):
		rest := strings.TrimPrefix(sourceRef, "refs/remotes/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 {
			return nil
		}
		remote = parts[0]
		if track == "inherit" {
			r, _ := c.GetConfig("branch."+parts[1]+".remote", "")
			m, _ := c.GetConfig("branch."+parts[1]+".merge", "")
			if r != "" {
				remote = r
			}
			if m != "" {
				mergeRef = m
			}
		}
		if mergeRef == "" {
			mergeRef = "refs/heads/" + parts[1]
		}
	case strings.HasPrefix(sourceRef, "refs/heads/"):
		remote = "."
		mergeRef = sourceRef
	default:
		return nil
	}

	cfgKey := "branch." + c.branchToCreate
	if err := c.SetConfig(cfgKey+".remote", remote); err != nil {
		return err
	}
	if err := c.SetConfig(cfgKey+".merge", mergeRef); err != nil {
		return err
	}
	c.trackRef = mergeRef
	c.trackRemote = remote
	return nil
}

func (c *GitCheckout) applyTreeToWorkdir(ctx context.Context, treeHash entities.Hash) error {
	newDiff, err := c.DiffHeadTree(ctx, treeHash)
	if err != nil {
		return err
	}
	newMap := make(map[string]*dirsync.DiffAction, len(newDiff))
	for i := range newDiff {
		d := &newDiff[i]
		newMap[d.Path] = d
	}

	if !c.Force {
		workDiff, err := c.DiffHeadWorkDir(ctx)
		if err != nil {
			return err
		}
		var overwrites []string
		for _, d := range workDiff {
			if _, found := newMap[d.Path]; found {
				overwrites = append(overwrites, d.Path)
			}
		}
		if len(overwrites) > 0 {
			msg := "error: Your local changes to the following files would be overwritten by checkout:\n\t"
			msg += strings.Join(overwrites, "\n\t")
			msg += "\nPlease move or remove them before you switch branches.\nAborting"
			return clierr.CLIErr("%s", msg)
		}
	}

	return dirsync.DirSyncDiff(ctx, c.casgit.store, c.workDir, newDiff)
}

func (c *GitCheckout) writeIndex(ctx context.Context, treeHash entities.Hash) error {
	mapObj, err := maps.Map(ctx, binding.NewDeepTreeRequest(treeHash, c.casgit.store))
	if err != nil {
		return err
	}
	deepTree, ok := mapObj.(*gitobjects.DeepTreeObject)
	if !ok {
		return fmt.Errorf("expected DeepTreeObject from tree %s", treeHash)
	}
	return c.casgit.WriteIndexFromDeepTree(c.absWorkTree, deepTree, path.Join(c.GitDir, "index"))
}

func (c *GitCheckout) updateHEAD() error {
	var content string
	if c.detached {
		content = c.targetCommit.String() + "\n"
	} else {
		content = "ref: " + c.headRef + "\n"
		refPath := path.Join(c.GitDir, c.headRef)
		if err := os.MkdirAll(filepath.Dir(refPath), 0755); err != nil {
			return err
		}
		if err := WriteFileAtomic(refPath, []byte(c.targetCommit.String()+"\n"), 0644); err != nil {
			return err
		}
	}
	return WriteFileAtomic(path.Join(c.GitDir, "HEAD"), []byte(content), 0644)
}

func (c *GitCheckout) writeOrigHead() error {
	if c.headHash.IsZero() {
		return nil
	}
	return WriteFileAtomic(path.Join(c.GitDir, "ORIG_HEAD"), []byte(c.headHash.String()+"\n"), 0644)
}

func (c *GitCheckout) printSwitchMessage(refName string, isBranch bool) {
	switch {
	case c.branchToCreate != "":
		fmt.Printf("Switched to a new branch %q\n", c.branchToCreate)
	case c.detached:
		fmt.Printf("HEAD is now at %s\n", c.targetCommit.String())
	case isBranch && strings.HasPrefix(refName, "refs/heads/"):
		fmt.Printf("Switched to branch %q\n", strings.TrimPrefix(refName, "refs/heads/"))
	default:
		fmt.Printf("Switched to %q\n", refName)
	}
}

func (c *GitCheckout) resolveTarget(ctx context.Context, target string) (commit *object.Commit, refName string, isBranch bool, err error) {
	hash, refName, err := c.findTarget(target)
	if err != nil {
		return nil, "", false, err
	}
	commit, err = c.casgit.ResolveCommit(ctx, hash)
	if err != nil {
		return nil, "", false, err
	}
	return commit, refName, strings.HasPrefix(refName, "refs/heads/"), nil

}

func (c *GitCheckout) findTarget(target string) (hash entities.Hash, refName string, err error) {
	if target == "" {
		return Hash{}, "", fmt.Errorf("missing checkout target")
	}

	if entities.IsHash(target) {
		return entities.NewHash(target), "", nil
	}

	if target == "HEAD" {
		if after, ok := strings.CutPrefix(c.head, "ref: "); ok {
			refName = after
		}
		return c.headHash, refName, nil
	}

	if ref := c.findRef(target); ref != "" {
		h, err := c.resolveRef(ref)

		return h, ref, err
	}

	if !c.NoGuess {
		var matches []string
		for ref := range c.refs {
			if strings.HasPrefix(ref, "refs/remotes/") && strings.HasSuffix(ref, "/"+target) {
				rest := strings.TrimPrefix(ref, "refs/remotes/")
				if !strings.HasSuffix(rest, "/HEAD") {
					matches = append(matches, ref)
				}
			}
		}
		sort.Strings(matches)
		if len(matches) == 1 {
			h, err := c.resolveRef(matches[0])
			if err != nil {
				return Hash{}, "", err
			}
			if c.branchToCreate == "" && !c.Detach {
				c.branchToCreate = target
				c.headRef = "refs/heads/" + target
			}
			return h, matches[0], nil
		}
		if len(matches) > 1 {
			return Hash{}, "", fmt.Errorf("%q matched multiple remote-tracking branches: %v", target, matches)
		}
	}

	h, err := c.RevParse(target)
	if err != nil || !entities.IsHash(h) {
		return Hash{}, "", fmt.Errorf("pathspec %q did not match any file(s) known to git", target)
	}
	return entities.NewHash(h), "", nil
}

func (c *GitCheckout) checkoutPaths(ctx context.Context) error {
	var treeHash entities.Hash
	target := c.Target
	if target == "" {
		target = "HEAD"
	}
	commit, _, _, err := c.resolveTarget(ctx, target)
	if err != nil {
		return err
	}
	treeHash = entities.Hash(commit.TreeHash)

	if treeHash.IsZero() {
		return fmt.Errorf("no source tree available for path checkout")
	}

	patterns, err := compilePathspecs(c.Paths, c.absWorkTree)
	if err != nil {
		return err
	}

	noOverlay := c.NoOverlay && !c.Overlay
	matched := make(map[string]bool)

	visit := func(p string, mode entities.FileMode, h entities.Hash) error {
		if !pathspecMatch(p, patterns) {
			return nil
		}
		matched[p] = true
		full := filepath.Join(c.absWorkTree, p)
		switch mode {
		case entities.FileModeDir:
			return os.MkdirAll(full, 0755)
		case entities.FileModeRegular, entities.FileModeExecutable, entities.FileModeDeprecated:
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				return err
			}
			if _, err := os.Stat(full); entities.IsNotExist(err) {
				if err := os.WriteFile(full, nil, 0644); err != nil {
					return err
				}
			}
			if err := bindcas.SetHash(full, h); err != nil {
				return err
			}
			perm := os.FileMode(0644)
			if mode == entities.FileModeExecutable {
				perm = 0755
			}
			return os.Chmod(full, perm)
		case entities.FileModeSymlink:
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				return err
			}
			data, err := c.casgit.store.GetObject(ctx, h)
			if err != nil {
				return err
			}
			_, body, err := entities.DecodeObject(data)
			if err != nil {
				return err
			}
			_ = os.Remove(full)
			return os.Symlink(string(body), full)
		case entities.FileModeSubmodule:
			return nil
		default:
			return nil
		}
	}

	if err := Walk(ctx, treeHash, c.casgit.store, visit); err != nil {
		return err
	}

	if noOverlay {
		if err := c.removeUnmatched(patterns, matched); err != nil {
			return err
		}
	}

	return c.writeIndex(ctx, treeHash)
}

func (c *GitCheckout) removeUnmatched(patterns []pathPattern, matched map[string]bool) error {
	return filepath.WalkDir(c.absWorkTree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(c.absWorkTree, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if !pathspecMatch(rel, patterns) {
			return nil
		}
		if !matched[rel] {
			return os.Remove(p)
		}
		return nil
	})
}

type pathPattern struct {
	raw     string
	negate  bool
	literal string
	isGlob  bool
}

func compilePathspecs(paths []string, workTree string) ([]pathPattern, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	out := make([]pathPattern, 0, len(paths))
	for _, p := range paths {
		neg := false
		if strings.HasPrefix(p, ":!") || strings.HasPrefix(p, ":(exclude)") {
			neg = true
			p = strings.TrimPrefix(p, ":!")
			p = strings.TrimPrefix(p, ":(exclude)")
		}
		p = strings.TrimPrefix(p, ":/")
		if p == "" {
			continue
		}
		var rel string
		if filepath.IsAbs(p) {
			r, err := filepath.Rel(workTree, p)
			if err != nil {
				return nil, err
			}
			rel = r
		} else {
			abs := filepath.Join(cwd, p)
			r, err := filepath.Rel(workTree, abs)
			if err != nil {
				return nil, err
			}
			rel = r
		}
		rel = filepath.ToSlash(rel)
		isGlob := strings.ContainsAny(rel, "*?[")
		out = append(out, pathPattern{
			raw:     rel,
			negate:  neg,
			literal: rel,
			isGlob:  isGlob,
		})
	}
	return out, nil
}

func pathspecMatch(p string, patterns []pathPattern) bool {
	if len(patterns) == 0 {
		return true
	}
	matched := false
	for _, pat := range patterns {
		ok := pathPatternMatches(p, pat)
		if ok {
			if pat.negate {
				return false
			}
			matched = true
		}
	}
	return matched
}

func pathPatternMatches(p string, pat pathPattern) bool {
	if pat.isGlob {
		ok, _ := path.Match(pat.literal, p)
		if ok {
			return true
		}
		ok, _ = path.Match(pat.literal, path.Base(p))
		return ok
	}
	if p == pat.literal {
		return true
	}
	if strings.HasPrefix(p, pat.literal+"/") {
		return true
	}
	return false
}
