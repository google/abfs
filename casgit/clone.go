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
	"strconv"
	"strings"

	"github.com/google/abfs/binding"
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
	if _, err := FindGitDir(c.Directory); err != nil && !entities.IsNotExist(err) {
		return err
	} else if err == nil {
		return clierr.CLIErr("cannot clone inside an existing git repository")
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

	for _, d := range baseDirs {
		// because there might be multiple .git in same level dir in repo setup
		needEmpty := d == c.Directory
		if created, err := checkDirForClone(d, needEmpty); err != nil {
			return err
		} else if created {
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

	if err := c.casgit.casfs.SetDeepTree(c.Directory, mapObj.GetHash()); err != nil {
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
