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

package gitindex

// git index parsing validation utility.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/google/abfs/git"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type gitIndexCmd struct {
	*root.RootCmd
	json        bool
	validate    bool
	useXattr    bool
	diff        bool
	quiet       bool
	checkStatus bool
	worktree    string
}

func NewCmd(rootCmd *root.RootCmd) *gitIndexCmd {
	return &gitIndexCmd{RootCmd: rootCmd}
}
func (c *gitIndexCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "git-index",
		Usage:   "[--json] [--validate] [-x] [-q] [--check-status] [--worktree dir] [--diff index1 index2] ",
		Aliases: []string{},
		Desc:    `Run validation on git index file`,
		Hidden:  true,
	}
}
func (c *gitIndexCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.json, "json", false, "Print out JSON version of index file")
	fl.BoolVar(&c.validate, "validate", false, "Run validation tests on index file")
	fl.BoolVarP(&c.useXattr, "use-xattr", "x", false, "Include xattr in validation")
	fl.BoolVar(&c.diff, "diff", false, "Diff two input index files")
	fl.BoolVarP(&c.quiet, "quiet", "q", false, "Only print top level errors")
	fl.BoolVar(&c.checkStatus, "check-status", false, "Run git status against worktree validating index isn't modified")
	fl.StringVar(&c.worktree, "worktree", "", "Include worktree for validation")
}
func (c *gitIndexCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	var indexFileName string
	var diffFileName string

	log.SetFlags(0)
	if c.quiet {
		os.Stdout = nil
	}
	filepath.Clean(c.worktree)

	if c.diff {
		indexFileName, diffFileName = args.EnsureTwoArgs(fl)
	} else {
		if fl.NArg() == 0 {
			if c.worktree == "" {
				return clierr.CLIUsage(fl, "No diff, worktree or index specified.")
			}

			// Take index from work tree
			indexFileName = filepath.Join(c.worktree, ".git", "index")
		} else {
			indexFileName = args.EnsureSingleArg(fl)
		}
	}

	// Create info
	var info indexInfo

	if err := info.Init(indexFileName); err != nil {
		return clierr.CLIErr("Index failure %s", err)
	}

	if c.checkStatus {
		if err := CheckStatus(c.worktree, indexFileName); err != nil {
			return clierr.CLIErr("Status check failed %s\n", err)
		}
	}

	if c.validate {
		if err := info.ValidateEntries(); err != nil {
			return clierr.CLIErr("Validation failed %s (%s)\n", err, info.path)
		}

		if c.worktree != "" {
			if err := info.ValidateWorktreeEntries(c.worktree, c.useXattr); err != nil {
				return clierr.CLIErr("Worktree entry validation failed %s (%s)\n", err, c.worktree)
			}
			if err := info.ValidateWorktree(c.worktree); err != nil {
				return clierr.CLIErr("Worktree validation failed %s (%s)\n", err, c.worktree)
			}
		}
	}
	if c.diff {

		var diffInfo indexInfo
		if err := diffInfo.Init(diffFileName); err != nil {
			return clierr.CLIErr("Failed to create diff index %s", err)
		}
		diffs, err := DiffIndexes(&info, &diffInfo)
		if err != nil {
			return clierr.CLIErr("Failed to diff %s and %s (%s)", indexFileName, diffFileName, err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "    ")
		if err := enc.Encode(diffs); err != nil {
			return clierr.CLIErr("Failed to encode diff %s", err)
		}
	} else if c.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "    ")
		if err := enc.Encode(info.gitIndex); err != nil {
			return clierr.CLIErr("Failed to encode git-index %s", err)
		}
	}
	return nil
}

// Remove items not used for compare
type Entry struct {
	Mode     uint32        `json:"mode"`
	FileSize uint32        `json:"filesize"`
	SHA1     git.IndexSha1 `json:"sha1"`
	Flags    uint16        `json:"flags"`
	Name     string        `json:"name"`
}

// Index structure for easier lookup and adding paths
// to tree items
type Index struct {
	Header   git.IndexHeader
	IndexMap map[string]Entry
	TreeMap  map[string]*git.CacheTreeEntry
}

type EntryDiff struct {
	A Entry `json:"entryA"`
	B Entry `json:"entryB"`
}
type TreeDiff struct {
	A *git.CacheTreeEntry `json:"treeA"`
	B *git.CacheTreeEntry `json:"treeB"`
}

type DiffData struct {
	FileA       string                `json:"fileA"`
	FileB       string                `json:"fileB"`
	Diffs       bool                  `json:"diffs"`
	HeaderA     *git.IndexHeader      `json:"headerA,omitempty"`
	HeaderB     *git.IndexHeader      `json:"headerB,omitempty"`
	EntryDiffs  []EntryDiff           `json:"entry_diffs,omitempty"`
	EntryNotInA []Entry               `json:"entry_not_in_a,omitempty"`
	EntryNotInB []Entry               `json:"entry_not_in_b,omitempty"`
	TreeDiffs   []TreeDiff            `json:"tree_diffs,omitempty"`
	TreeNotInA  []*git.CacheTreeEntry `json:"tree_not_in_a,omitempty"`
	TreeNotInB  []*git.CacheTreeEntry `json:"tree_not_in_b,omitempty"`
}

func (index *Index) updateCacheTreeMap(gitIndex *git.Index, dir string, idx *int) {
	e := gitIndex.CacheTreeEntries[*idx]
	eName := filepath.Join(dir, e.Path)
	// Add to map with full path
	if _, ok := index.TreeMap[eName]; ok {
		fmt.Printf("Tree already contains %s", eName)
	}
	index.TreeMap[eName] = e
	// Add any subtrees
	for i := 0; i < e.Subtrees(); i++ {
		*idx++
		index.updateCacheTreeMap(gitIndex, eName, idx)
	}

}
func NewIndex(gitIndex *git.Index) *Index {
	index := &Index{
		Header:   gitIndex.Header,
		IndexMap: make(map[string]Entry),
		TreeMap:  make(map[string]*git.CacheTreeEntry),
	}

	// Not supporting staging entries
	for _, e := range gitIndex.IndexEntries {
		index.IndexMap[e.Name] = Entry{
			Mode:     e.Mode,
			FileSize: e.FileSize,
			SHA1:     e.SHA1,
			Flags:    e.Flags,
			Name:     e.Name,
		}
	}

	for idx := 0; idx < len(gitIndex.CacheTreeEntries); {
		index.updateCacheTreeMap(gitIndex, "", &idx)
		idx++
	}
	return index
}

// Info structure
type indexInfo struct {
	gitIndex *git.Index
	index    *Index
	fi       fs.FileInfo
	path     string
}

func (info *indexInfo) Index() *Index {
	if info.index == nil {
		info.index = NewIndex(info.gitIndex)
	}
	return info.index
}
func (info *indexInfo) GitIndex() *git.Index {
	return info.gitIndex
}

// Validate entries times compared to index file
func (info *indexInfo) ValidateWorktreeEntries(dir string, checkxattr bool) error {
	errors := 0
	for k, e := range info.Index().IndexMap {
		filename := filepath.Join(dir, k)
		fi, err := os.Lstat(filename)
		if err != nil {
			errors++
			fmt.Printf("Failed to stat entry file  %s\n", filename)
			continue
		}
		if e.Mode != uint32(entities.FileModeSubmodule) {
			if fi.IsDir() {
				errors++
				fmt.Printf("Found directory %s in entries\n", filename)
			}
		}

		if checkxattr {
			// No checks on submodules
			// Only check loaded for files (expect dirs and symlinks to be loaded)
			loaded, _ := bindcas.Loaded(filename)
			hash, _ := bindcas.Hash(filename)

			checkLoaded := false
			checkHash := false

			switch e.Mode & syscall.S_IFMT {
			case syscall.S_IFREG:
				checkLoaded = true
				checkHash = true

				// Since git status could have been run need to ignore
				// .git files it reads
				switch filepath.Base(k) {
				case ".gitignore":
					fallthrough
				case ".gitmodules":
					checkLoaded = false
				}
			case syscall.S_IFLNK:
				checkHash = true
			case uint32(entities.FileModeSubmodule):
			}

			if checkLoaded && loaded {
				errors++
				fmt.Printf("File %s is loaded\n", filename)
			}
			if checkHash && hash.String() != e.SHA1.String() {
				errors++
				fmt.Printf("File %s index hash %s doesn't match read value %s\n",
					filename, e.SHA1.String(), hash)
			}
		}
	}
	if errors != 0 {
		return fmt.Errorf("detected %d entry errors", errors)
	}
	return nil
}

// Validate the working tree matches what is in the index file.
func (info *indexInfo) ValidateWorktree(dir string) error {
	// Validate working dir exists
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("(%s) does not exist", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("(%s) is not a directory", dir)
	}

	errors := 0

	// Validate that entries in the index are valid (exists)
	for k, t := range info.Index().TreeMap {
		fullDir := filepath.Join(dir, k)
		fi, err = os.Lstat(fullDir)
		if err != nil {
			errors++
			fmt.Printf("Index entry %s does not exists in working tree\n", k)
			continue
		}
		if !fi.IsDir() {
			errors++
			fmt.Printf("Index entry %s is not a directory in working tree\n", k)
			continue
		}

		fileCount := 0
		dirCount := 0
		expFileCount := t.Entries()
		expDirCount := t.Subtrees()

		err := filepath.Walk(fullDir, func(path string, fi fs.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if excludedName(fi.Name()) {
				return filepath.SkipDir
			}
			// Is this a submodule ?
			if fi.IsDir() {
				if e, ok := info.Index().IndexMap[strings.TrimPrefix(path, dir+"/")]; ok {
					if e.Mode == uint32(entities.FileModeSubmodule) {
						fileCount++
						return filepath.SkipDir
					}
				}

				if fullDir != path {
					if fi, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
						if fi.IsDir() {
							return filepath.SkipDir
						}
					}

				}
				if filepath.Dir(path) == fullDir {
					dirCount++
				}
			} else {
				fileCount++
				if _, ok := info.Index().IndexMap[strings.TrimPrefix(path, dir+"/")]; !ok {
					errors++
					fmt.Printf("File exists in tree %s not in index\n", path)
				}
			}
			return nil
		})
		if err != nil {
			errors++
			fmt.Printf("Failed to walk path %s\n", fullDir)
			continue
		}

		if fileCount != expFileCount {
			errors++
			fmt.Printf("File count in workspace %s %d mismatch to index %d\n", fullDir, fileCount, expFileCount)
		}
		if dirCount != expDirCount {
			errors++
			fmt.Printf("Subtree count in workspace %s %d mismatch to index %d\n", fullDir, dirCount, expDirCount)
		}

	}
	if errors != 0 {
		return fmt.Errorf("detected %d errors", errors)
	}
	return nil
}

func excludedName(name string) bool {
	return (name == ".") || (name == "..") || (name == ".git")
}

func (info *indexInfo) ValidateEntries() error {
	errors := 0

	for idx, e := range info.GitIndex().IndexEntries {
		switch e.Mode & syscall.S_IFMT {
		case syscall.S_IFREG:
			if e.FileSize == 0 {
				// Check that the index file does not contain files with 0 size
				// but non-zero hash
				sha := e.SHA1.String()
				if sha != entities.EmptyFileHash.String() {
					errors++
					fmt.Printf("%s size is 0 but hash is non-empty %s\n", e.Name, sha)
				}
			}
		case syscall.S_IFLNK:
		case uint32(entities.FileModeSubmodule):
			// Check that submodule reports a commit hash (i.e. non-empty dir)
			sha := e.SHA1.String()
			if sha == entities.EmptyDirHash.String() {
				errors++
				fmt.Printf("%s sub-module contains empty dir hash \n", e.Name)
			}

		default:
			errors++
			fmt.Printf("%s errors mode %o\n", e.Name, e.Mode)
		}
		// "Index entries are sorted in ascending order on the name field,
		//  interpreted as a string of unsigned bytes."
		// Not checking same name for staged files here.
		if idx != 0 {
			if e.Name < info.GitIndex().IndexEntries[idx-1].Name {
				errors++
				fmt.Printf("%s out of order \n", e.Name)
			}
		}

		// "The special path components ".", ".." and ".git" (without quotes) are disallowed.
		// Trailing slash is also disallowed.

		if excludedName(e.Name) {
			errors++
			fmt.Printf("%s name is not allowed", e.Name)
		}

		if e.Name[0] == '/' || e.Name[len(e.Name)-1] == '/' {
			errors++
			fmt.Printf("%s slash at start or end not allowed", e.Name)
		}
	}
	if errors != 0 {
		return fmt.Errorf("detected %d entry errors", errors)
	}
	return nil
}

func createDiffData(fileA, fileB string) *DiffData {
	return &DiffData{
		FileA: fileA,
		FileB: fileB,
		Diffs: false,
	}
}

func DiffIndexes(aInfo, bInfo *indexInfo) (*DiffData, error) {
	out := createDiffData(aInfo.path, bInfo.path)
	a := aInfo.Index()
	b := bInfo.Index()

	if a.Header != b.Header {
		out.Diffs = true
		out.HeaderA = &a.Header
		out.HeaderB = &b.Header
	}

	for k, vA := range a.IndexMap {
		if vB, ok := b.IndexMap[k]; ok {
			if vA != vB {
				out.EntryDiffs = append(out.EntryDiffs, EntryDiff{A: vA, B: vB})
			}
		} else {
			out.EntryNotInB = append(out.EntryNotInB, vA)
		}

	}
	for k, vB := range b.IndexMap {
		if _, ok := a.IndexMap[k]; !ok {
			out.EntryNotInA = append(out.EntryNotInA, vB)
		}
	}

	for k, vA := range a.TreeMap {
		if vB, ok := b.TreeMap[k]; ok {
			if *vA != *vB {
				out.TreeDiffs = append(out.TreeDiffs, TreeDiff{A: vA, B: vB})
			}
		} else {
			out.TreeNotInB = append(out.TreeNotInB, vA)
		}

	}
	for k, vB := range b.TreeMap {
		if _, ok := a.TreeMap[k]; !ok {
			out.TreeNotInA = append(out.TreeNotInA, vB)
		}
	}

	return out, nil
}

func CheckStatus(workdir, filename string) error {
	fi, err := os.Stat(filename)
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "status")
	cmd.Dir = workdir

	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to run git status %s (%s)", workdir, err)
	}
	fi2, err := os.Stat(filename)
	if err != nil {
		return err
	}

	// Make sure index isn't modified
	if fi.ModTime() != fi2.ModTime() {
		return fmt.Errorf("index <%s> modified from running git status", filename)
	}
	return nil
}

func (info *indexInfo) Init(filename string) (retErr error) {
	info.path = filename
	fi, err := os.Stat(filename)
	if err != nil {
		return fmt.Errorf("failed to stat input file %s (%s)", filename, err)
	}
	info.fi = fi
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open input file %s (%s)", filename, err)
	}
	defer func() { retErr = entities.FirstError(retErr, file.Close()) }()
	info.gitIndex = git.NewIndex(0)

	err = info.gitIndex.Read(file, fi.Size())
	if err != nil {
		return fmt.Errorf("failed to read input index file %s (%s)", filename, err)
	}
	return nil
}
