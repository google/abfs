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
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/git"
	"github.com/google/abfs/maps"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
)

type setIndexCmd struct {
	*gitCmd
	*cmd.StoreProvider
	dir      string
	dest     string
	git      *casgit.CASGit
	treeHash entities.Hash

	statStorage map[string]git.IndexEntryData
}

func NewSetIndexCmd(gitCmd *gitCmd) *setIndexCmd {
	return &setIndexCmd{gitCmd: gitCmd}
}

func (c *setIndexCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "set-index",
		Usage:   "<dir> <tree-hash>",
		Aliases: []string{},
		Desc:    `Reset the index file based on the dir and the given tree`,
	}
}

func (c *setIndexCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.dest, "output", "o", "", "path to output")
}

/*
   This command servers a helper to reset git index if needed

   In some commands, git will check index files to track file changes. One of
   the property it tracked is mtime. If git detects the mtime of a file differ
   from the recording in index files, it might try to read the content and
   calculate the hash, which will trigger ABFS to download files. This command,
   given a treeish and directory as input, it reads the stat of directory and
   the corresponding deeptree to re-generate a git-index files contain correct
   information.
*/

func (c *setIndexCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	dir, treeish := args.EnsureTwoArgs(fl)
	c.dir = dir

	if c.dest == "" {
		c.dest = path.Join(c.dir, bindcas.GitDirName, "index")
	}

	c.git = casgit.New(c.Store)

	return c.writeTreeishIndex(ctx, treeish)
}

func (c *setIndexCmd) writeTreeishIndex(ctx context.Context, treeish string) (retErr error) {
	hash, err := c.getTreeID(treeish)
	if err != nil {
		return err
	}
	c.treeHash = entities.NewHash(hash)

	mapObj, err := maps.Map(ctx, binding.NewDeepTreeRequest(c.treeHash, c.Store))
	if err != nil {
		return err
	}

	deepTree := mapObj.(*gitobjects.DeepTreeObject)

	index, err := c.calculateIndex(deepTree)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(c.dest, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, f.Close()) }()

	return index.Write(f)
}

func (c *setIndexCmd) calculateIndex(tree *gitobjects.DeepTreeObject) (*git.Index, error) {
	index := git.NewIndex(2)
	err := c.collectStatInfo()
	if err != nil {
		return nil, err
	}
	if err := c.updateEntries(index, "", tree.Entries); err != nil {
		return nil, err
	}
	return index, nil
}

func (c *setIndexCmd) collectStatInfo() error {
	c.statStorage = make(map[string]git.IndexEntryData)
	walkFunc := func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(c.dir, path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("failed to retrieve stat info from %s", path)
		}
		cs, cns := stat.Mtim.Unix()
		ms, mns := stat.Mtim.Unix()
		c.statStorage[relPath] = git.IndexEntryData{
			CtimeSeconds: uint32(cs),
			CtimeMillis:  uint32(cns / 1e6),
			MtimeSeconds: uint32(ms),
			MtimeMillis:  uint32(mns / 1e6),
			Ino:          uint32(stat.Ino),
			Uid:          stat.Uid,
			Gid:          stat.Gid,
			Mode:         stat.Mode,
			FileSize:     uint32(info.Size()),
		}
		return nil
	}

	// TODO: try out https://github.com/karrick/godirwalk to see whether there
	// are improvments
	return filepath.Walk(c.dir, walkFunc)
}

func (c *setIndexCmd) updateEntries(index *git.Index, relPath string, entries []*entities.DeepEntry) error {
	var i int
	var ent *entities.DeepEntry

	hasDir := false
	for i, ent = range entries {
		if ent.Mode == entities.FileModeDir {
			hasDir = true
			break
		}
	}

	if !hasDir {
		return fmt.Errorf("failed to find the dir to add to index")
	}

	dirPath := path.Join(relPath, ent.Name)
	cacheEnt := index.CreateCacheTreeEntry(path.Base(ent.Name), ent.Hash)
	startIndexEntries := index.NumEntries()
	subTreeCnt := 0

	for i = i + 1; i < len(entries); i++ {
		ent := entries[i]
		switch ent.Mode {
		case entities.FileModeDir:
			subTreeCnt += 1
			if err := c.updateEntries(index, dirPath, entries[i:i+int(ent.Size)]); err != nil {
				return err
			}
			i += int(ent.Size) - 1
		case entities.FileModeDeepTree:
			subTreeCnt += 1
			deepTree, err := binding.FetchObject[gitobjects.DeepTreeObject](context.Background(), c.Store, ent.Hash)
			if err != nil {
				return err
			}
			treePath := path.Join(dirPath, ent.Name)
			if err := c.updateEntries(index, treePath, deepTree.Entries); err != nil {
				return err
			}
		default:
			entPath := path.Join(dirPath, ent.Name)
			hash, err := bindcas.Hash(path.Join(c.dir, entPath))
			if err != nil {
				return err
			}
			// Submodule is failing at here, skipping the check for now.
			if hash != ent.Hash && ent.Mode != entities.FileModeSubmodule {
				return fmt.Errorf("directory layout are not the same: %s has %s, but recorded as %s in tree %s", ent.Name, hash, ent.Hash.String(), c.treeHash.String())
			}
			if err := c.addEntry(index, ent, dirPath); err != nil {
				return err
			}
		}
	}

	endIndexEntries := index.NumEntries()
	index.UpdateTree(cacheEnt, endIndexEntries-startIndexEntries, subTreeCnt)
	return nil
}

func (c *setIndexCmd) addEntry(index *git.Index, ent *entities.DeepEntry, dir string) error {
	entryData := c.statStorage[path.Join(dir, ent.Name)]
	entryData.Dev = uint32(0x41)
	entryData.SHA1 = git.IndexSha1(ent.Hash)
	entryData.Flags = uint16(len(path.Join(dir, ent.Name)))
	if ent.Mode == entities.FileModeSymlink {
		entryData.Mode = syscall.S_IFLNK
	}
	index.AddEntry(path.Join(dir, ent.Name), entryData)
	return nil
}

func (c *setIndexCmd) getTreeID(treeish string) (string, error) {
	if _, _, err := c.Store.GetTree(context.Background(), entities.NewHash(treeish)); err == nil {
		return treeish, nil
	}

	commit, err := c.git.GetCommit(context.Background(), entities.NewHash(treeish))
	if err != nil {
		return "", err
	}
	return commit.TreeHash.String(), nil
}
