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

package diff

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"sort"

	"github.com/spf13/pflag"

	"github.com/google/abfs/cas/cas"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	bcas "github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
)

type diffCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	showParents bool
	out         io.Writer
}

func NewCmd(rootCmd *root.RootCmd) *diffCmd {
	return &diffCmd{
		RootCmd: rootCmd,
		out:     os.Stdout,
	}
}

func (c *diffCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "diff",
		Usage:   "[-p] <hash-1> <hash-2>",
		Aliases: []string{},
		Desc:    `Show the diff between two hashes`,
	}
}

func (c *diffCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.showParents, "show-parents", "p", false, "If the hash is a tree, show the parent directory changes as well.")
}

func (c *diffCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	hexHash1, hexHash2 := args.EnsureTwoArgs(fl)

	var hash1, hash2 entities.Hash
	if _, err := hex.Decode(hash1[:], []byte(hexHash1)); err != nil {
		return err
	}
	if _, err := hex.Decode(hash2[:], []byte(hexHash2)); err != nil {
		return err
	}

	return c.diff(ctx, hash1, hash2)
}

func (c *diffCmd) Printf(format string, a ...any) {
	_, _ = fmt.Fprintf(c.out, format, a...)
}

func (c *diffCmd) Println(a ...any) {
	_, _ = fmt.Fprintln(c.out, a...)
}

func (c *diffCmd) diff(ctx context.Context, hash1, hash2 entities.Hash) error {
	type1, err := c.getType(ctx, hash1)
	if err != nil {
		return fmt.Errorf("cannot get type of %s: %w", hash1, err)
	}
	type2, err := c.getType(ctx, hash2)
	if err != nil {
		return fmt.Errorf("cannot get type of %s: %w", hash2, err)
	}

	if type1 != type2 {
		return fmt.Errorf("the specified objects types do not match (%s != %s)", type1, type2)
	}

	switch entities.ObjectType(type1) {
	case entities.ObjectTypeBlob:
		return c.diffBlobs(ctx, hash1, hash2)

	case gitobjects.ObjectTypeDir:
		return c.diffTrees(hash1, hash2)

	case gitobjects.ObjectTypeCommit:
		return c.diffCommits(ctx, hash1, hash2)

	case binding.ObjectTypeEvalRequest:
		return c.diffEvalRequest(ctx, hash1, hash2)

	case binding.ObjectTypeEvalResponse:
		return c.diffEvalResponses(ctx, hash1, hash2)

	case binding.ObjectTypeEnvVars:
		return c.diffEnvVars(ctx, hash1, hash2)

	case binding.ObjectTypeWSSnapshot:
		return c.diffWSSnapshot(ctx, hash1, hash2)

	case binding.ObjectTypeIndexSnapObject:
		return c.diffIndexSnap(ctx, hash1, hash2)

	default:
		return fmt.Errorf("diff for type %s is not supported", type1)
	}
}

// TODO add get-tpe as a functionality to store.
func (c *diffCmd) getType(ctx context.Context, hash entities.Hash) (string, error) {
	buf, err := c.Store.GetObject(ctx, hash)
	if err != nil {
		reader, err := c.Store.OpenBlob(ctx, hash)
		if err != nil {
			return "", err
		}
		if err := reader.Close(); err != nil {
			return "", err
		}
		return "blob", nil
	}
	t, _, err := entities.DecodeObject(buf)
	if err != nil {
		return "", err
	}
	return t, nil
}

func (c *diffCmd) addPatch(action Action, p *entities.PatchEntry) {
	c.Printf("%s %s %s %12d %s\n", action, p.Mode, p.Hash, p.Size, p.Path)
}

func (c *diffCmd) diffPatch(p1, p2 entities.Patch) {
	// both p1 and p2 are sorted by the Path, so we can iterate them similar to merge sort
	i1, i2 := 0, 0
	for i1 < len(p1) || i2 < len(p2) {
		if i1 == len(p1) || ((i2 < len(p2)) && p1[i1].Path > p2[i2].Path) {
			c.addPatch(ActionAdd, p2[i2])
			i2++
		} else if i2 == len(p2) || p2[i2].Path > p1[i1].Path {
			c.addPatch(ActionRemove, p1[i1])
			i1++
		} else {
			if !reflect.DeepEqual(p1[i1], p2[i2]) {
				c.addPatch(ActionRemove, p1[i1])
				c.addPatch(ActionAdd, p2[i2])
			}
			i1++
			i2++
		}
	}
}

func (c *diffCmd) diffEvalResponses(ctx context.Context, hash1, hash2 entities.Hash) error {
	resp1, err := binding.FetchObject[binding.EvalResponse](ctx, c.Store, hash1)
	if err != nil {
		return err
	}
	resp2, err := binding.FetchObject[binding.EvalResponse](ctx, c.Store, hash2)
	if err != nil {
		return err
	}
	binding.DiffRunInfo(c.out, resp1, resp2)
	if !reflect.DeepEqual(resp1.ReadPatch, resp2.ReadPatch) {
		c.Println("Reads:")
		c.diffPatch(resp1.ReadPatch, resp2.ReadPatch)
	}
	if !reflect.DeepEqual(resp1.WritePatch, resp2.WritePatch) {
		c.Println("Writes:")
		c.diffPatch(resp1.WritePatch, resp2.WritePatch)
	}
	return nil
}

func (c *diffCmd) diffEvalRequest(ctx context.Context, hash1, hash2 entities.Hash) error {
	req1, err := binding.FetchObject[binding.EvalRequest](ctx, c.Store, hash1)
	if err != nil {
		return err
	}
	req2, err := binding.FetchObject[binding.EvalRequest](ctx, c.Store, hash2)
	if err != nil {
		return err
	}
	if req1.Command != req2.Command {
		c.Println("Command:")
		c.Printf("%s %s\n", ActionAdd, req1.Command)
		c.Printf("%s %s\n", ActionRemove, req2.Command)
	}
	if req1.EnvVars != req2.EnvVars {
		c.Println("Env Vars:")
		if err := c.diffEnvVars(ctx, req1.EnvVars, req2.EnvVars); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(req1.InputPatch, req2.InputPatch) {
		c.Println("Inputs:")
		c.diffPatch(req1.InputPatch, req2.InputPatch)
	}

	return nil
}

func (c *diffCmd) diffEnvVars(ctx context.Context, hash1, hash2 entities.Hash) error {
	vars1, err := binding.FetchObject[binding.EnvVars](ctx, c.Store, hash1)
	if err != nil {
		return err
	}
	vars2, err := binding.FetchObject[binding.EnvVars](ctx, c.Store, hash2)
	if err != nil {
		return err
	}

	allKeys := make(map[string]struct{})
	for k := range vars1.Vars {
		allKeys[k] = struct{}{}
	}
	for k := range vars2.Vars {
		allKeys[k] = struct{}{}
	}

	sortedKeys := make([]string, 0, len(allKeys))
	for k := range allKeys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	for _, k := range sortedKeys {
		v1, ok1 := vars1.Vars[k]
		v2, ok2 := vars2.Vars[k]

		if ok1 && !ok2 {
			c.Printf("%s %s=%s\n", ActionRemove, k, v1)
		} else if !ok1 && ok2 {
			c.Printf("%s %s=%s\n", ActionAdd, k, v2)
		} else if v1 != v2 {
			c.Printf("%s %s=%s\n", ActionRemove, k, v1)
			c.Printf("%s %s=%s\n", ActionAdd, k, v2)
		}
	}

	return nil
}

type Namer interface {
	Name() string
}

func (c *diffCmd) diffBlobs(ctx context.Context, hash1, hash2 entities.Hash) error {
	// The objects are already fetched to get their types
	blob1, err := c.Store.OpenBlob(ctx, hash1)
	if err != nil {
		return fmt.Errorf("failed to open blob %s: %w", hash1, err)
	}
	defer func() {
		if err := blob1.Close(); err != nil {
			fmt.Printf("failed to close blob %s (%s)\n", hash1, err)
		}
	}()
	namer1, ok := blob1.(Namer)
	if !ok {
		return fmt.Errorf("expected the blob reader %s to support Name() (type=%T)", hash1, blob1)
	}

	blob2, err := c.Store.OpenBlob(ctx, hash2)
	if err != nil {
		return fmt.Errorf("failed to open blob %s: %w", hash2, err)
	}
	defer func() { _ = blob2.Close() }()
	namer2, ok := blob2.(Namer)
	if !ok {
		return fmt.Errorf("expected the blob reader %s to support Name() (type=%T)", hash2, blob2)
	}

	cmd := exec.CommandContext(ctx, "diff", "--color", namer1.Name(), namer2.Name())
	cmd.Stdout = c.out
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func (c *diffCmd) diffCommits(ctx context.Context, hash1, hash2 entities.Hash) error {
	git := casgit.New(c.Store)

	commit1, err := git.GetCommit(ctx, hash1)
	if err != nil {
		return err
	}
	commit2, err := git.GetCommit(ctx, hash2)
	if err != nil {
		return err
	}

	return c.diffTrees(entities.Hash(commit1.TreeHash), entities.Hash(commit2.TreeHash))
}

func (c *diffCmd) diffTrees(hash1, hash2 entities.Hash) error {
	dh := func(action cas.DiffAction, dirPath string, de *cas.DirEntry) {
		act := ActionRemove
		if action == cas.DiffActionAdd {
			act = ActionAdd
		}
		c.addPathItem(act, bcas.ABFSMode(de.Mode), bcas.ABFSHash(de.Hash), dirPath+"/"+de.Name)
	}
	return cas.Diff(bcas.CASHash(&hash1), bcas.CASHash(&hash2), c.Store.CAS(), c.showParents, dh)
}

type Action int

const (
	ActionAdd Action = iota
	ActionRemove
)

func (a Action) String() string {
	switch a {
	case ActionAdd:
		return "+"
	case ActionRemove:
		return "-"
	default:
		panic(fmt.Sprintf("unknown action %d", a))
	}
}

func (c *diffCmd) addPathItem(action Action, mode entities.FileMode, hash entities.Hash, path string) {
	c.Printf("%s %s %s %s\n", action, entities.ModeToString(mode), hash, path)
}

func (c *diffCmd) diffWSSnapshot(ctx context.Context, hash1, hash2 entities.Hash) error {
	snap1, err := binding.FetchObject[binding.WSSnapshot](ctx, c.Store, hash1)
	if err != nil {
		return err
	}
	snap2, err := binding.FetchObject[binding.WSSnapshot](ctx, c.Store, hash2)
	if err != nil {
		return err
	}

	if snap1.Snapshot.Root != snap2.Snapshot.Root {
		c.Printf("Root:\n\t%s %s\n", snap1.Snapshot.Root, snap2.Snapshot.Root)
		c.Println()
	}

	diffRepo := func(r1, r2 binding.GitRepoSnapshot) {
		c.Printf("Git Repo: %s\n", r1.WorkDir)
		if r1.WorkDirHash != r2.WorkDirHash {
			c.Printf("\tWork Hash\t%s %s\n", r1.WorkDirHash, r2.WorkDirHash)
		}
		if r1.GitDir != r2.GitDir {
			c.Printf("\tGit Dir\t%s %s\n", r1.GitDir, r2.GitDir)
		}
		if r1.GitDirHash != r2.GitDirHash {
			c.Printf("\tGit Dir Hash\t%s %s\n", r1.GitDirHash, r2.GitDirHash)
		}
		if r1.ObjectsDir != r2.ObjectsDir {
			c.Printf("\tObjects Dir\t%s %s\n", r1.ObjectsDir, r2.ObjectsDir)
		}
		if r1.PacksHash != r2.PacksHash {
			c.Printf("\tPacks Hash\t%s %s\n", r1.PacksHash, r2.PacksHash)
		}
		if r1.IndexHash != r2.IndexHash {
			c.Printf("\tIndex Hash\t%s %s\n", r1.IndexHash, r2.IndexHash)
		}
		c.Println()
	}

	workDirs := make(map[string]struct{})
	gitRepo1 := make(map[string]binding.GitRepoSnapshot, len(snap1.Snapshot.GitRepos))
	for _, r := range snap1.Snapshot.GitRepos {
		gitRepo1[r.WorkDir] = r
		workDirs[r.WorkDir] = struct{}{}
	}
	gitRepo2 := make(map[string]binding.GitRepoSnapshot, len(snap2.Snapshot.GitRepos))
	for _, r := range snap2.Snapshot.GitRepos {
		gitRepo2[r.WorkDir] = r
		workDirs[r.WorkDir] = struct{}{}
	}

	for w := range workDirs {
		if gitRepo1[w] != gitRepo2[w] {
			diffRepo(gitRepo1[w], gitRepo2[w])
		}
	}

	return nil
}

func (c *diffCmd) diffIndexSnap(ctx context.Context, hash1, hash2 entities.Hash) error {
	snap1, err := binding.FetchObject[binding.IndexSnapObject](ctx, c.Store, hash1)
	if err != nil {
		return err
	}
	snap2, err := binding.FetchObject[binding.IndexSnapObject](ctx, c.Store, hash2)
	if err != nil {
		return err
	}

	diffEntries := func(name string, r1, r2 *binding.IndexSnapEntry) {
		c.Printf("File: %s\n", name)
		if r1 != nil {
			c.Printf("\t- %s %15d %7o\n", r1.Hash, r1.Size, r1.Mode)
		}
		if r2 != nil {
			c.Printf("\t+ %s %15d %7o\n", r2.Hash, r2.Size, r2.Mode)
		}
	}

	paths := make([]string, 0, len(snap1.Entries)+len(snap2.Entries))
	entriesMap1 := make(map[string]*binding.IndexSnapEntry, len(snap1.Entries))
	for i, r := range snap1.Entries {
		entriesMap1[r.Name] = &snap1.Entries[i]
		paths = append(paths, r.Name)
	}
	entriesMap2 := make(map[string]*binding.IndexSnapEntry, len(snap2.Entries))
	for i, r := range snap2.Entries {
		entriesMap2[r.Name] = &snap2.Entries[i]
		paths = append(paths, r.Name)
	}

	slices.Sort(paths)
	paths = slices.Compact(paths)

	for _, p := range paths {
		r1 := entriesMap1[p]
		r2 := entriesMap2[p]
		if r1 == nil || r2 == nil || *r1 != *r2 {
			diffEntries(p, r1, r2)
		}
	}

	return nil
}
