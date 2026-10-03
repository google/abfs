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

package daemon

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/google/abfs/cas/fs/gitindex"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/maps"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
)

func (i *indexSnapCache) isValid(current *indexSnapCache) bool {
	if current.indexMTime.After(i.indexMTime) {
		return false
	}
	if i.headTreeHash != current.headTreeHash {
		return false
	}
	return true
}

type headFile struct {
	Hash entities.Hash
	Mode entities.FileMode
	Size int64
}

func (c *SnapshotService) resolveHeadTree(ctx context.Context, gitRepo *gitRepo) (entities.Hash, error) {
	headHash, err := resolveHead(path.Join(c.mountPoint, gitRepo.gitDir))
	if err != nil {
		return entities.Hash{}, fmt.Errorf("error reading HEAD of %q: %w", gitRepo.gitDir, err)
	}

	commit, err := c.git.ResolveCommit(ctx, headHash)
	if err != nil {
		return entities.Hash{}, err
	}
	return entities.Hash(commit.TreeHash), nil
}

func (c *SnapshotService) currentSnapState(gitRepo *gitRepo) (*indexSnapCache, error) {
	indexPath := gitRepo.IndexPath(c.mountPoint)

	cache := new(indexSnapCache)

	fi, err := os.Stat(indexPath)
	if err != nil {
		if !entities.IsNotExist(err) {
			return nil, fmt.Errorf("error reading index file %q: %w", indexPath, err)
		}
	} else {
		cache.indexMTime = fi.ModTime()
	}

	cache.headTreeHash = gitRepo.cachedHeadTree
	return cache, nil
}

func (c *SnapshotService) SnapIndex(ctx context.Context, gitRepo *gitRepo) (entities.Hash, error) {
	indexPath := gitRepo.IndexPath(c.mountPoint)

	cached := &gitRepo.indexSnap

	current, err := c.currentSnapState(gitRepo)
	if err != nil {
		return entities.Hash{}, err
	}

	if cached.isValid(current) {
		return cached.snapHash, nil
	}

	snapObj, err := c.snapIndex(ctx, indexPath, current.headTreeHash)
	if err != nil {
		return entities.Hash{}, err
	}

	// Cache the result
	newCache := indexSnapCache{
		indexMTime:   current.indexMTime,
		headTreeHash: current.headTreeHash,
		snapHash:     snapObj.Hash,
		entries:      len(snapObj.Entries),
	}

	gitRepo.indexSnap = newCache

	return snapObj.Hash, nil
}

func (c *SnapshotService) snapIndex(ctx context.Context, indexPath string, headTreeHash entities.Hash) (*binding.IndexSnapObject, error) {
	var indexEntries []gitindex.IndexEntry
	res, err := gitindex.Read(indexPath)
	if err != nil {
		if !entities.IsNotExist(err) {
			return nil, err
		}
	} else {
		indexEntries = res.Entries
	}

	headFiles := make(map[string]headFile)
	mapObj, err := maps.Map(ctx, binding.NewDeepTreeRequest(headTreeHash, c.store))
	if err != nil {
		return nil, fmt.Errorf("failed to map HEAD deeptree for %s: %w", headTreeHash, err)
	}
	deepTree := mapObj.(*gitobjects.DeepTreeObject)
	if err := collectDeepTreeFiles(ctx, c.store, "", deepTree.Entries, headFiles); err != nil {
		return nil, fmt.Errorf("failed to collect files from deeptree: %w", err)
	}

	var entries []binding.IndexSnapEntry
	seen := make(map[string]bool)

	for _, indexEnt := range indexEntries {
		indexHash := entities.Hash(indexEnt.Sha)
		headEnt, exists := headFiles[indexEnt.Path]
		if exists {
			seen[indexEnt.Path] = true
			if indexHash == headEnt.Hash {
				// Exclude from IndexSnapObject if the hash is the same as the file on HEAD
				continue
			}
		}

		// Include in IndexSnapObject representing addition or modification
		entries = append(entries, binding.IndexSnapEntry{
			Name: indexEnt.Path,
			Hash: indexHash,
			Size: int64(indexEnt.Size),
			Mode: indexEnt.Mode,
		})
	}

	// For files in HEAD that are not in the index, include them as removed
	for headPath := range headFiles {
		if !seen[headPath] {
			entries = append(entries, binding.IndexSnapEntry{
				Name: headPath,
				Hash: entities.ZeroHash,
				Size: 0,
				Mode: 0,
			})
		}
	}

	snapObj := &binding.IndexSnapObject{
		Version: binding.CurrentIndexSnapObjectVersion,
		Entries: entries,
	}

	if err := snapObj.Encode(); err != nil {
		return nil, fmt.Errorf("failed to encode IndexSnapObject: %w", err)
	}

	if err := c.store.PutObject(ctx, snapObj.Hash, snapObj.Encoded); err != nil {
		return nil, fmt.Errorf("failed to put IndexSnapObject in store: %w", err)
	}
	return snapObj, nil
}

func (c *SnapshotService) RestoreIndex(ctx context.Context, indexSnapHash entities.Hash, gitRepo *gitRepo) error {
	if gitRepo.indexSnap.snapHash == indexSnapHash && gitRepo.indexSnap.headTreeHash == gitRepo.cachedHeadTree {
		return nil
	}

	workDir := path.Join(c.mountPoint, gitRepo.workDir)
	indexPath := gitRepo.IndexPath(c.mountPoint)

	var res *gitindex.Result
	if !gitRepo.cachedHeadTree.IsZero() {
		deepTree, err := c.deepTreeForTree(ctx, gitRepo.cachedHeadTree)
		if err != nil {
			mapObj, err := maps.Map(ctx, binding.NewDeepTreeRequest(gitRepo.cachedHeadTree, c.store))
			if err != nil {
				return fmt.Errorf("failed to map HEAD deeptree for %s: %w", gitRepo.cachedHeadTree, err)
			}
			deepTree = mapObj.(*gitobjects.DeepTreeObject)
		}

		treeDir := casgit.NewDeepTreeDir(c.git, deepTree)
		var scanErr error
		res, scanErr = gitindex.ScanTree(ctx, workDir, treeDir, 64*1024)
		if scanErr != nil {
			return fmt.Errorf("failed to scan tree for index restore: %w", scanErr)
		}
	} else {
		res = &gitindex.Result{
			Entries: make([]gitindex.IndexEntry, 0),
			Trees:   make([]*gitindex.TreeEntry, 0),
		}
	}

	snapObj, err := binding.FetchObject[binding.IndexSnapObject](ctx, c.store, indexSnapHash)
	if err != nil {
		return fmt.Errorf("failed to fetch IndexSnapObject %s: %w", indexSnapHash, err)
	}

	if len(snapObj.Entries) > 0 {
		applyIndexSnap(snapObj, res)
	}

	if err := bindcas.WriteGitIndex(indexPath, res); err != nil {
		return fmt.Errorf("failed to write restored index: %w", err)
	}

	fi, err := os.Stat(indexPath)
	if err != nil {
		return fmt.Errorf("failed to stat restored index: %w", err)
	}
	gitRepo.indexSnap = indexSnapCache{
		indexMTime:   fi.ModTime(),
		headTreeHash: gitRepo.cachedHeadTree,
		snapHash:     snapObj.Hash,
		entries:      len(snapObj.Entries),
	}

	return nil
}

func applyIndexSnap(snapObj *binding.IndexSnapObject, res *gitindex.Result) {
	indexMap := make(map[string]*gitindex.IndexEntry, len(res.Entries))
	for i := range res.Entries {
		ent := &res.Entries[i]
		indexMap[ent.Path] = ent
	}

	for _, snapEnt := range snapObj.Entries {
		if snapEnt.Hash.IsZero() {
			delete(indexMap, snapEnt.Name)
		} else {
			if ent, found := indexMap[snapEnt.Name]; found {
				ent.Sha = [20]byte(snapEnt.Hash)
				ent.Size = uint32(snapEnt.Size)
				ent.Mode = snapEnt.Mode
			} else {
				indexMap[snapEnt.Name] = &gitindex.IndexEntry{
					Path: snapEnt.Name,
					Sha:  [20]byte(snapEnt.Hash),
					Size: uint32(snapEnt.Size),
					Mode: snapEnt.Mode,
				}
			}
		}
	}

	newEntries := make([]gitindex.IndexEntry, 0, len(indexMap))
	for _, indexEnt := range indexMap {
		newEntries = append(newEntries, *indexEnt)
	}
	sort.Slice(newEntries, func(i, j int) bool {
		return newEntries[i].Path < newEntries[j].Path
	})
	res.Entries = newEntries
}

func collectDeepTreeFiles(ctx context.Context, store entities.CASStore, relPath string, entries []*entities.DeepEntry, files map[string]headFile) error {
	if len(entries) == 0 {
		return nil
	}

	// Verify that the first entry is a directory
	dirEnt := entries[0]
	if !dirEnt.Mode.IsDir() && dirEnt.Mode != entities.FileModeDeepTree {
		return fmt.Errorf("expected directory entry at root of deep tree, got mode %o", dirEnt.Mode)
	}

	dirPath := relPath
	if dirEnt.Name != "" {
		dirPath = path.Join(relPath, dirEnt.Name)
	}

	for i := 1; i < len(entries); i++ {
		ent := entries[i]
		switch ent.Mode {
		case entities.FileModeDir:
			// Resolve this subdirectory recursively
			subTreeSize := int(ent.Size)
			if i+subTreeSize > len(entries) {
				return fmt.Errorf("malformed deep tree: subdirectory size %d exceeds total entries %d", subTreeSize, len(entries))
			}
			if err := collectDeepTreeFiles(ctx, store, dirPath, entries[i:i+subTreeSize], files); err != nil {
				return err
			}
			// Skip all transitive descendants under this directory
			i += subTreeSize - 1

		case entities.FileModeDeepTree:
			// Retrieve the nested DeepTree chunk and walk it recursively
			deepTree, err := binding.FetchObject[gitobjects.DeepTreeObject](ctx, store, ent.Hash)
			if err != nil {
				return err
			}
			treePath := dirPath
			if ent.Name != "" {
				treePath = path.Join(dirPath, ent.Name)
			}
			if err := collectDeepTreeFiles(ctx, store, treePath, deepTree.Entries, files); err != nil {
				return err
			}

		default:
			// regular file, executable, symlink, submodule
			entPath := path.Join(dirPath, ent.Name)
			files[entPath] = headFile{
				Hash: ent.Hash,
				Mode: ent.Mode,
				Size: ent.Size,
			}
		}
	}
	return nil
}

func resolveHead(gitDir string) (entities.Hash, error) {
	headPath := path.Join(gitDir, "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		return entities.ZeroHash, err
	}
	headContent := strings.TrimSpace(string(data))
	return resolveRef(gitDir, headContent)
}

func resolveRef(gitDir string, refContent string) (entities.Hash, error) {
	visited := make(map[string]bool)
	return resolveRefRecursive(gitDir, refContent, visited, 0)
}

func resolveRefRecursive(gitDir string, refContent string, visited map[string]bool, depth int) (hash entities.Hash, retErr error) {
	if depth > 10 {
		return entities.ZeroHash, fmt.Errorf("depth limit exceeded during ref resolution")
	}
	refContent = strings.TrimSpace(refContent)
	if entities.IsHash(refContent) {
		return entities.NewHash(refContent), nil
	}

	refPath, ok := strings.CutPrefix(refContent, "ref: ")
	if !ok {
		return entities.ZeroHash, fmt.Errorf("invalid ref format: %s", refContent)
	}
	if visited[refPath] {
		return entities.ZeroHash, fmt.Errorf("ref loop detected on %s", refPath)
	}
	visited[refPath] = true

	refFile := path.Join(gitDir, refPath)
	if data, err := os.ReadFile(refFile); err == nil {
		return resolveRefRecursive(gitDir, string(data), visited, depth+1)
	} else if !entities.IsNotExist(err) {
		return entities.ZeroHash, err
	}

	packedPath := path.Join(gitDir, "packed-refs")
	f, err := os.Open(packedPath)
	if err != nil {
		if entities.IsNotExist(err) {
			return entities.ZeroHash, entities.NotExistErrf("ref %q not found", refPath)
		}
		return entities.ZeroHash, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == refPath {
			return resolveRefRecursive(gitDir, parts[0], visited, depth+1)
		}
	}
	if err := scanner.Err(); err != nil {
		return entities.ZeroHash, err
	}
	return entities.ZeroHash, entities.NotExistErrf("ref %q not found in packed-refs", refPath)
}
