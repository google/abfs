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
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/cas/fs/gitindex"
	"github.com/google/abfs/filesystem/dirsync"
	"github.com/google/abfs/maps"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
)

const (
	RepoPacksDir = "packs"
	RepoDirName  = ".repo"
)

type cloneSource int

const (
	cloneSourceBranch cloneSource = iota
	cloneSourceTag
	cloneSourceCommit
)

type CloneOptions struct {
	RemoteRepo       string
	Directory        string
	Origin           string
	Commit           entities.Hash
	TreeHash         entities.Hash
	Branch           string
	SingleBranch     bool
	NoTags           bool
	GitDir           string
	ObjectsDir       string
	Quiet            bool
	KeepOnFail       bool
	Nested           bool
	PrefetchIndices  bool
	PrefetchSymlinks bool
	DetachedHead     bool
}

type GitClone struct {
	CloneOptions
	*GitRepo

	absDirectory string
	commitHash   entities.Hash
	cloneSource  cloneSource
	branchRef    string
	sourceHash   entities.Hash
}

func (g *CASGit) Clone(ctx context.Context, opts CloneOptions) error {
	if opts.GitDir == "" {
		opts.GitDir = path.Join(opts.Directory, ".git")
	}
	if opts.ObjectsDir == "" {
		opts.ObjectsDir = path.Join(opts.GitDir, "objects")
	}

	gitRepo := NewGitRepo(g, opts.Directory, opts.GitDir, opts.ObjectsDir)

	clone := &GitClone{
		CloneOptions: opts,
		GitRepo:      gitRepo,
	}

	return clone.clone(ctx)
}

func (c *GitClone) clone(ctx context.Context) (returnErr error) {
	// A nested clone sits inside the work tree of another repository, as a repo project whose
	// path lies inside another project's path.
	if c.Nested {
		if slices.Contains(strings.Split(filepath.ToSlash(c.Directory), "/"), ".git") {
			return clierr.CLIErr("cannot clone into %s: the path has a .git element", c.Directory)
		}
	} else {
		if _, err := FindGitDir(c.Directory); err != nil && !entities.IsNotExist(err) {
			return err
		} else if err == nil {
			return clierr.CLIErr("cannot clone inside an existing git repository")
		}
	}

	absDir, err := filepath.Abs(c.Directory)
	if err != nil {
		return err
	}
	c.absDirectory = absDir

	baseDirs := []string{
		c.Directory,
		c.GitDir,
		path.Dir(c.ObjectsDir),
	}

	createdDir := false
	for _, d := range baseDirs {
		// because there might be multiple .git in same level dir in repo setup
		needEmpty := d == c.Directory && !c.Nested
		if created, err := checkDirForClone(d, needEmpty); err != nil {
			return err
		} else if created {
			if d == c.Directory {
				createdDir = true
			}
			defer func() {
				if returnErr != nil && !c.KeepOnFail {
					if err := os.RemoveAll(d); err != nil {
						log.Printf("failed to remove directory %s: %v", d, err)
					}
				}
			}()
		}
	}

	if err := c.casgit.casfs.SetExcluded(c.Directory, true); err != nil {
		return err
	}
	if c.Nested && !createdDir {
		// The directory stays after a failed clone and can hold files of the work tree around
		// it, so it goes back into that work tree's hash.
		defer func() {
			if returnErr != nil {
				if err := c.casgit.casfs.SetExcluded(c.Directory, false); err != nil {
					log.Printf("failed to include directory %s: %v", c.Directory, err)
				}
			}
		}()
	}
	if err := c.casgit.casfs.CreateGitObjects(path.Dir(c.ObjectsDir), path.Base(c.ObjectsDir)); err != nil {
		return err
	}
	if err := c.casgit.casfs.SetExcluded(c.gitDir, true); err != nil {
		return err
	}

	if !c.Quiet {
		fmt.Printf("Cloning %s branch %s into %q...\n", c.RemoteRepo, c.Branch, c.Directory)
	}

	if err := c.setRemoteURL(c.RemoteRepo); err != nil {
		return err
	}

	if !c.Commit.IsZero() {
		c.cloneSource = cloneSourceCommit
		c.sourceHash = c.Commit
	} else if c.Branch == "" {
		if err := c.setDefaultBranch(ctx); err != nil {
			return err
		}
	}

	if c.cloneSource != cloneSourceCommit {
		if err := c.resolveBranch(ctx); err != nil {
			return err
		}
	}

	commit, err := c.casgit.ResolveCommit(ctx, c.sourceHash)
	if err != nil {
		return err
	}
	c.commitHash = entities.Hash(commit.Hash)

	treeHash := entities.Hash(commit.TreeHash)
	if !c.TreeHash.IsZero() {
		treeHash = c.TreeHash
	}

	mapObj, err := maps.Map(ctx, binding.NewDeepTreeRequest(treeHash, c.casgit.store))
	if err != nil {
		return fmt.Errorf("cannot create deeptree for project %s (tree=%s): %w", c.Directory, treeHash, err)
	}

	deepTree := mapObj.(*gitobjects.DeepTreeObject)

	if err := c.setWorkTree(ctx, NewDeepTreeDir(c.casgit, deepTree), mapObj.GetHash()); err != nil {
		return err
	}

	indexPath := path.Join(c.GitDir, "index")
	if err := c.casgit.WriteIndexFromDeepTree(c.absDirectory, deepTree, indexPath); err != nil {
		return err
	}

	var head string
	if c.cloneSource != cloneSourceBranch || c.DetachedHead {
		head = commit.Hash.String()
	} else {
		head = fmt.Sprintf("ref: %s", c.branchRef)
	}

	if err := os.WriteFile(path.Join(c.GitDir, "HEAD"), []byte(head+"\n"), 0644); err != nil {
		return err
	}

	if err := c.writeConfig(); err != nil {
		return err
	}

	if err := c.writeRefs(); err != nil {
		return err
	}

	if err := c.addPacks(ctx); err != nil {
		return err
	}

	if err := c.casgit.client.AddGitRepo(c.workDir, c.gitDir, c.objectsDir, treeHash); err != nil {
		return err
	}
	if err := c.casgit.client.LoadTreeDigestMap(ctx, treeHash); err != nil {
		log.Printf("cannot load digest map for project %s (tree=%s): %v", c.Directory, treeHash, err)
	}

	if c.PrefetchSymlinks {
		if err := binding.PrefetchSymlinks(ctx, deepTree, c.casgit.store, c.casgit.cacheman); err != nil {
			log.Printf("cannot prefetch symlinks for project %s: %v", c.remoteURL, err)
		}
	}

	return nil
}

func (c *GitClone) resolveBranch(ctx context.Context) error {
	hash, branchRef, err := c.casgit.ResolveRef(ctx, c.server, c.project, c.Branch)
	if err != nil {
		return err
	}
	c.branchRef = branchRef
	c.sourceHash = hash
	if strings.HasPrefix(branchRef, "refs/tags/") {
		c.cloneSource = cloneSourceTag
	} else {
		c.cloneSource = cloneSourceBranch
	}

	if c.NoTags && c.cloneSource == cloneSourceTag {
		hash = entities.ZeroHash
	}
	if hash.IsZero() {
		return fmt.Errorf("remote branch %s not found in upstream %s/%s", c.Branch, c.server, c.project)
	}
	if c.cloneSource == cloneSourceBranch && !strings.HasPrefix(c.branchRef, "refs/heads/") {
		return fmt.Errorf("remote branch %s is not a branch", c.Branch)
	}
	return nil
}

func (c *GitClone) setDefaultBranch(ctx context.Context) error {
	ref, err := c.casgit.GetSymRef(ctx, c.server, c.project, "HEAD")
	if err != nil {
		return err
	}

	if ref == "" {
		// TODO: git checks global config for `init.defaultBranch` first.
		c.Branch = "master"
	} else {
		c.Branch = strings.TrimPrefix(ref, "refs/heads/")
	}

	return nil
}

// setWorkTree sets the deeptree of the work tree. A nested clone first clears what is in the way
// of the tree, as casfs keeps an existing entry on a name clash. When the directory still holds
// entries, it adds the missing entries of the tree instead: casfs sets the hash of every existing
// directory a deeptree reaches to the tree's, so the entries kept there would drop out of the
// work tree hashes and of the workspace snapshot.
func (c *GitClone) setWorkTree(ctx context.Context, tree gitindex.TreeDirectory, deepTree entities.Hash) error {
	if c.Nested {
		if err := clearTreePaths(ctx, c.Directory, tree); err != nil {
			return fmt.Errorf("failed to clear the work tree %s: %w", c.Directory, err)
		}
		entries, err := os.ReadDir(c.Directory)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			missing, err := missingTreeEntries(ctx, c.casgit.store, c.Directory, "", entities.Hash(tree.Sha()))
			if err != nil {
				return fmt.Errorf("failed to list the missing entries of the work tree %s: %w", c.Directory, err)
			}
			return dirsync.DirSyncDiff(ctx, c.casgit.store, c.Directory, missing)
		}
	}
	return c.casgit.casfs.SetDeepTree(c.Directory, deepTree)
}

// missingTreeEntries returns, as actions that add them, the entries of the tree that are missing
// at rel in dir. It goes into the directories that are there and keeps every existing entry.
func missingTreeEntries(ctx context.Context, store entities.CASStore, dir, rel string, treeHash entities.Hash) ([]dirsync.DiffAction, error) {
	content, meta, err := store.GetTree(ctx, treeHash)
	if err != nil {
		return nil, err
	}
	entries, err := entities.DecodeDir(content, meta)
	if err != nil {
		return nil, err
	}
	var missing []dirsync.DiffAction
	for _, e := range entries {
		p := path.Join(rel, e.Name)
		stat, err := os.Lstat(filepath.Join(dir, p))
		if entities.IsNotExist(err) {
			missing = append(missing, dirsync.DiffAction{Path: p, HashB: e.Hash, ModeB: e.Mode, SizeB: e.Size})
			continue
		} else if err != nil {
			return nil, err
		}
		if e.Mode.IsDir() && e.Hash != entities.EmptyDirHash && stat.IsDir() {
			sub, err := missingTreeEntries(ctx, store, dir, p, e.Hash)
			if err != nil {
				return nil, err
			}
			missing = append(missing, sub...)
		}
	}
	return missing, nil
}

// RepositoryInWorkTreeError is returned by a nested clone whose tree has a file where the work
// tree has a directory that holds another repository, at the directory or below it.
type RepositoryInWorkTreeError struct {
	// Path is the directory where the tree has a file.
	Path string
	// Repository is the work tree of the repository at or below Path.
	Repository string
}

var _ error = (*RepositoryInWorkTreeError)(nil)

func (e *RepositoryInWorkTreeError) Error() string {
	return fmt.Sprintf("cannot check out a file at %s: it would replace the repository at %s", e.Path, e.Repository)
}

// clearTreePaths removes the entries of dir that are in the way of tree and keeps the others, as
// git read-tree --reset -u does when repo checks out a project into a directory that holds files
// of the project around it. Directories that hold another repository are kept, as repo keeps them
// when it removes a project.
func clearTreePaths(ctx context.Context, dir string, tree gitindex.TreeDirectory) error {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return err
	}
	files, err := tree.Files(ctx)
	if err != nil {
		return err
	}
	subtrees, err := tree.Subtrees(ctx)
	if err != nil {
		return err
	}
	inTree := make(map[string]gitindex.TreeDirectory, len(files)+len(subtrees))
	for _, f := range files {
		inTree[f.Name] = nil
	}
	for _, s := range subtrees {
		inTree[s.Name()] = s
	}
	for _, e := range entries {
		sub, ok := inTree[e.Name()]
		if !ok {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if sub != nil {
				if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
					continue
				}
				if err := clearTreePaths(ctx, p, sub); err != nil {
					return err
				}
				continue
			}
			repo, err := findRepository(p)
			if err != nil {
				return err
			}
			if repo != "" {
				return &RepositoryInWorkTreeError{Path: p, Repository: repo}
			}
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

// findRepository returns the first directory at or below dir that holds a .git, or "" if there
// is none.
func findRepository(dir string) (string, error) {
	var repo string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			repo = p
			return filepath.SkipAll
		} else if !entities.IsNotExist(err) {
			return err
		}
		return nil
	})
	return repo, err
}

// returns true if directory was created
func checkDirForClone(dir string, requireEmpty bool) (bool, error) {
	stat, err := os.Stat(dir)
	if err != nil {
		if !entities.IsNotExist(err) {
			return false, err
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("failed to create directory %s: %v", dir, err)
		}
		return true, nil
	}
	if !stat.IsDir() {
		return false, clierr.CLIErr("%s is not a directory", dir)
	}
	if requireEmpty {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return false, fmt.Errorf("failed to read directory %s: %v", dir, err)
		}
		if len(ents) > 0 {
			return false, clierr.CLIErr("%s is not empty", dir)
		}
	}
	return false, nil
}

func (c *GitClone) writeRefs() error {
	if err := os.Mkdir(path.Join(c.GitDir, "refs"), 0755); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
	}

	refs, err := c.getRefs()
	if err != nil {
		return err
	}

	if err := c.casgit.writePackedRefs(c.GitDir, refs); err != nil {
		return err
	}

	if c.cloneSource == cloneSourceBranch && !c.DetachedHead {
		refDir := path.Dir(path.Join(c.GitDir, c.branchRef))
		if err := os.MkdirAll(refDir, 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path.Join(c.GitDir, c.branchRef), []byte(c.commitHash.String()+"\n"), 0644); err != nil {
			return err
		}
	}

	return nil
}

func (c *GitClone) getRefs() (map[string]string, error) {
	refs := make(map[string]string)

	if !c.SingleBranch {
		heads, err := c.casgit.client.Store().SearchRefs(c.server, c.project, "refs/heads/", 0)
		if err != nil {
			return nil, err
		}
		for _, ref := range heads {
			refName := strings.TrimPrefix(ref.Ref, "refs/heads/")
			refs[fmt.Sprintf("refs/remotes/%s/%s", c.Origin, refName)] = ref.Hash.String()
		}
	} else if c.Branch != "" {
		refs[fmt.Sprintf("refs/remotes/%s/%s", c.Origin, c.Branch)] = c.commitHash.String()
	}

	if !c.NoTags {
		tags, err := c.casgit.client.Store().SearchRefs(c.server, c.project, "refs/tags/", 0)
		if err != nil {
			return nil, err
		}
		for _, tag := range tags {
			refs[tag.Ref] = tag.Hash.String()
		}
	}
	return refs, nil
}

func (c *GitClone) writeConfig() (returnErr error) {
	f, err := os.OpenFile(path.Join(c.GitDir, "config"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}

	defer func() {
		if err := f.Close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()

	type KV struct {
		Key   string
		Value string
	}

	config := []KV{
		{"core.repositoryformatversion", "0"},
		{"core.filemode", "true"},
		{"core.bare", "false"},
		{"core.logallrefupdates", "true"},
		{"core.compression", "0"},
		{"core.looseCompression", "0"},
		{"core.abbrev", "40"},

		{"fetch.unpackLimit", "2147483647"},
		{"gc.auto", "0"},
		{"gc.packRefs", "false"},
		{"maintenance.loose-object.auto", "0"},
		{"maintenance.incremental-repack.auto", "0"},
		{"casfs.prefetch-indicies", strconv.FormatBool(c.PrefetchIndices)},
		{"casfs.prefetch-symlinks", strconv.FormatBool(c.PrefetchSymlinks)},
	}

	var fetchSpec string
	if c.SingleBranch && c.cloneSource == cloneSourceTag {
		fetchSpec = fmt.Sprintf("%s:%s", c.branchRef, c.branchRef)
	} else if c.SingleBranch && c.cloneSource != cloneSourceCommit {
		fetchSpec = fmt.Sprintf("%s:refs/remotes/%s/%s", c.branchRef, c.Origin, c.Branch)
	} else {
		fetchSpec = fmt.Sprintf("refs/heads/*:refs/remotes/%s/*", c.Origin)
	}

	config = append(config, KV{fmt.Sprintf("remote.%s.url", c.Origin), c.RemoteRepo})
	config = append(config, KV{fmt.Sprintf("remote.%s.fetch", c.Origin), fetchSpec})

	if c.NoTags {
		config = append(config, KV{fmt.Sprintf("remote.%s.tagOpt", c.Origin), "--no-tags"})
	}

	if c.cloneSource == cloneSourceBranch && !c.DetachedHead {
		config = append(config, KV{fmt.Sprintf("branch.%s.remote", c.Branch), c.Origin})
		config = append(config, KV{fmt.Sprintf("branch.%s.merge", c.Branch), c.branchRef})
	}

	gitConfig := new(GitConfig)
	for _, kv := range config {
		if err := gitConfig.Add(kv.Key, kv.Value); err != nil {
			return err
		}
	}

	return WriteGitConfig(c.GitDir, gitConfig)
}
