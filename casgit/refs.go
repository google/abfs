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
	"bufio"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/abfs/shared/entities"
)

// readRefs returns all the refs (both loose and from packs) that match the given refspecs.
// If specs is empty, it returns all refs.
// A ref is kept if it matches at least one positive refspec and no negative refspecs.
func (g *CASGit) readRefs(gitDir string, specs []RefSpec) (map[string]string, error) {
	allRefs, err := g.readPackedRefs(gitDir)
	if err != nil {
		return nil, err
	}

	//TODO do smarter scan based on positive refspecs
	looseRefs, err := g.readLooseDir(gitDir, "refs")
	if err != nil {
		return nil, err
	}

	maps.Copy(allRefs, looseRefs)

	if len(specs) == 0 {
		return allRefs, nil
	}

	filteredRefs := make(map[string]string)
	for ref, hash := range allRefs {
		if _, ok := RefSpecsMatch(ref, specs); ok {
			filteredRefs[ref] = hash
		}
	}
	return filteredRefs, nil
}

func (g *CASGit) readLooseDir(gitDir string, dir string) (map[string]string, error) {
	p := path.Join(gitDir, dir)
	stat, err := os.Stat(p)
	if err != nil {
		return nil, entities.IgnoreNotExistErr(err)
	}
	if !stat.IsDir() {
		h, err := g.readLooseRef(gitDir, dir)
		if err != nil {
			return nil, err
		}
		return map[string]string{dir: h}, nil
	}
	allRefs := make(map[string]string)
	err = filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(gitDir, path)
		if err != nil {
			return err
		}

		h, err := g.readLooseRef(gitDir, relPath)
		if err != nil {
			return err
		}
		allRefs[relPath] = h
		return nil
	})
	if err != nil {
		return nil, err
	}
	return allRefs, nil
}

func (g *CASGit) readLooseRef(gitDir string, ref string) (string, error) {
	content, err := os.ReadFile(filepath.Join(gitDir, ref))
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(content)), nil
}

func (g *CASGit) readPackedRefs(gitDir string) (refs map[string]string, retErr error) {
	packedRefsPath := path.Join(gitDir, packedRefsFile)
	refs = make(map[string]string)
	f, err := os.Open(packedRefsPath)
	if err != nil {
		if entities.IsNotExist(err) {
			return refs, nil
		}
		return nil, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 2 {
			refs[parts[1]] = parts[0]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return refs, nil
}

func (g *CASGit) writePackedRefs(gitDir string, refs map[string]string) (retErr error) {
	f, err := os.OpenFile(path.Join(gitDir, packedRefsFile), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open packed-refs for writing: %w", err)
	}
	defer entities.CloseErrCheck(f, &retErr)

	buf := bufio.NewWriter(f)
	if _, err := fmt.Fprintln(buf, "# pack-refs with: peeled fully-peeled sorted "); err != nil {
		return err
	}

	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, ref := range keys {
		if _, err := fmt.Fprintf(buf, "%s %s\n", refs[ref], ref); err != nil {
			return err
		}
	}
	return buf.Flush()
}

func (g *CASGit) updateRef(gitDir string, ref string, newHash entities.Hash) error {
	return WriteFileAtomic(path.Join(gitDir, ref), []byte(newHash.String()+"\n"), 0644)
}

func WriteFileAtomic(filename string, data []byte, perm os.FileMode) (retErr error) {
	dir, name := filepath.Split(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tempFile, err := os.CreateTemp(dir, "."+name+"*.temp")
	if err != nil {
		return err
	}
	defer func() {
		entities.CloseErrCheck(tempFile, &retErr)
		_ = os.Remove(tempFile.Name())
	}()

	if err := os.WriteFile(tempFile.Name(), data, perm); err != nil {
		return err
	}

	return os.Rename(tempFile.Name(), filename)
}
