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
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/google/abfs/shared/entities"
)

func (s *ShellGit) RevListAll(repoPath string) ([]entities.Hash, error) {
	reader, close, err := s.streamingGitCommand(repoPath, "rev-list", "--all")
	if err != nil {
		return nil, err
	}
	closed := false
	defer func() {
		if !closed { // happens on error
			if err := close(); err != nil {
				log.Printf("Error while closing git rev-list command: %s", err)
			}
		}
	}()

	scanner := bufio.NewScanner(reader)
	scanner.Split(bufio.ScanLines)
	hashes := make([]entities.Hash, 0)
	for scanner.Scan() {
		hashes = append(hashes, entities.NewHash(scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	closed = true
	return hashes, close()
}

// RevListObjects just clarifies blobs vs. objects slices
type RevListObjects struct {
	Blobs   []entities.Hash
	Objects []entities.Hash
}

// RevListObjects utilizes `git rev-list --objects` to find all objects that are descendants of
// the provided `includedCommits`, but are not descendants of the provided `excludedCommits`.
// This effectively provides all new objects between commits.
func (s *ShellGit) RevListObjects(repoPath string, includedCommits, excludedCommits []entities.Hash) (*RevListObjects, error) {
	// the filter causes the blobs to still show in the output, but have a ~ character in front of them
	stdin := make([]string, 0, len(includedCommits)+len(excludedCommits))
	for _, commitToInclude := range includedCommits {
		stdin = append(stdin, commitToInclude.String())
	}
	for _, commitToExclude := range excludedCommits {
		stdin = append(stdin, fmt.Sprintf("^%s", commitToExclude.String()))
	}

	commandArgs := []string{"rev-list", "--objects", "--no-object-names", "--filter=blob:none", "--filter-print-omitted", "--topo-order", "--stdin"}
	stdout, close, err := s.streamingGitCommandStdin(repoPath, strings.Join(stdin, "\n"), commandArgs...)
	if err != nil {
		return nil, err
	}
	objects, err := parseRevListObjectsOutput(stdout)
	if err != nil {
		if err := close(); err != nil {
			log.Printf("Error while closing git rev-list command: %s", err)
		}
		return nil, err
	}
	if err := close(); err != nil {
		return nil, err
	}

	return objects, nil
}

// parseRevListObjectsOutput expects the format:
// <object-id>
// <object-id>
// ~<blob-id>
func parseRevListObjectsOutput(input io.Reader) (*RevListObjects, error) {
	scanner := bufio.NewScanner(input)
	scanner.Split(bufio.ScanLines)
	b := make([]entities.Hash, 0)
	o := make([]entities.Hash, 0)
	for scanner.Scan() {
		token := scanner.Text()
		if token[:1] == "~" {
			b = append(b, entities.NewHash(token[1:]))
		} else {
			o = append(o, entities.NewHash(token))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("`git rev-list` output scanning returned an error: %w", err)
	}
	return &RevListObjects{
		Blobs:   b,
		Objects: o,
	}, nil
}

// revListObjectsTopoOrderRaw outputs objects reachable from rev but unreachable
// from the exclude set in topological order in raw bytes to the writer.
func (s *ShellGit) revListObjectsTopoOrderRaw(repoPath, rev string, excludes []entities.Hash, writer io.Writer) error {
	stderr := new(bytes.Buffer)
	args := []string{
		"rev-list",
		"--objects",
		"--topo-order",
		"--in-commit-order",
		"--no-object-names",
		"--reverse",
		rev,
	}
	if len(excludes) > 0 {
		args = append(args, "--not")
		args = append(args, excludesForCommand(excludes)...)
	}

	cmd := exec.Command(s.BinPath, args...)
	cmd.Dir = repoPath
	cmd.Stdout = writer
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to get topo order revisions in directory %q.\nstderr:%s\nerr: %v", repoPath, stderr.String(), err)
	}

	return nil
}

type ObjectTypeName struct {
	ObjType plumbing.ObjectType
	Name    entities.Hash
}

type ObjectTypeNameStreamItem struct {
	*ObjectTypeName
	Err error
}

// RevListObjectsTopoOrder traverses the object tree from `rev` and provides
// output with the following characteristics:
//   - commit objects are output in reverse topological order. Parent commits
//     will be before children commits.
//   - tree and blob objects immediately following a commit are output in DFS
//     order.
//   - The output is a partial tree. Tree and blob objects that are present in
//     multiple trees are only shown once, which is at first node traversal in
//     DFS. They will not be shown if they are reachable from an excluded
//     commit.
//   - It is not guaranteed that observed objects from `cat-file` or `ls-tree`
//     will appear at all expected nodes in the tree.
func (s *ShellGit) RevListObjectsTopoOrder(repoPath, rev string, excludes []entities.Hash) (<-chan ObjectTypeNameStreamItem, error) {
	revListRead, revListWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	catFileRead, catFileWrite, err := os.Pipe()
	if err != nil {
		if err := revListRead.Close(); err != nil {
			log.Printf("failed to close rev list read pipe: %v", err)
		}
		if err := revListWrite.Close(); err != nil {
			log.Printf("failed to close rev list write pipe: %v", err)
		}
		return nil, err
	}

	out := make(chan ObjectTypeNameStreamItem)
	// Stream out raw rev-list results.
	go func() {
		defer func() {
			if err := revListWrite.Close(); err != nil {
				log.Printf("failed to close rev list write pipe: %v", err)
			}
		}()

		if err := s.revListObjectsTopoOrderRaw(repoPath, rev, excludes, revListWrite); err != nil {
			out <- ObjectTypeNameStreamItem{Err: err}
		}
	}()

	// Stream out raw batch-check results.
	go func() {
		defer func() {
			if err := revListRead.Close(); err != nil {
				log.Printf("failed to close rev list read pipe: %v", err)
			}
			if err := catFileWrite.Close(); err != nil {
				log.Printf("failed to close cat file write pipe: %v", err)
			}
		}()

		if err := s.batchCheckObjectInfoRaw(repoPath, revListRead, catFileWrite); err != nil {
			out <- ObjectTypeNameStreamItem{Err: err}
		}
	}()

	// Stream out structured results.
	go func() {
		defer func() {
			if err := catFileRead.Close(); err != nil {
				log.Printf("failed to close cat file read pipe: %v", err)
			}
			close(out)
		}()
		scanner := bufio.NewScanner(catFileRead)
		for scanner.Scan() {
			split := strings.Fields(scanner.Text())
			if len(split) != 2 {
				out <- ObjectTypeNameStreamItem{Err: fmt.Errorf("expecting 2 fields present in %s", scanner.Text())}
				return
			}
			objType, err := plumbing.ParseObjectType(split[0])
			if err != nil {
				out <- ObjectTypeNameStreamItem{Err: err}
				return
			}
			name := plumbing.NewHash(split[1])
			out <- ObjectTypeNameStreamItem{&ObjectTypeName{objType, entities.Hash(name)}, nil}
		}
		if err := scanner.Err(); err != nil {
			out <- ObjectTypeNameStreamItem{Err: err}
		}
	}()

	return out, nil
}
