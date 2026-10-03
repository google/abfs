// Copyright 2025 Google LLC
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

package tests

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/fs/gitindex"
	"golang.org/x/sync/errgroup"
)

type gitTest struct {
	fs  fs.FS
	bin string
}

func gitError(stdout, stderr []string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("commit fail %v \nstdout:\n%s\nstderr:\n%s", err,
		strings.Join(stdout, "\n"),
		strings.Join(stderr, "\n"))

}

func (g *gitTest) RunCmdInDir(root string, args ...string) (stdout []string, stderr []string, err error) {
	stdoutB := new(bytes.Buffer)
	stderrB := new(bytes.Buffer)
	cmd := exec.Command(g.bin, args...)
	cmd.Dir = root
	cmd.Stdout = stdoutB
	cmd.Stderr = stderrB

	err = cmd.Run()
	scanner := bufio.NewScanner(stdoutB)
	for scanner.Scan() {
		stdout = append(stdout, scanner.Text())
	}
	scanner = bufio.NewScanner(stderrB)
	for scanner.Scan() {
		stderr = append(stderr, scanner.Text())
	}

	return stdout, stderr, err
}

func (g *gitTest) InitEmpty(root string) error {
	return gitError(g.RunCmdInDir(root, "init", "."))

}
func (g *gitTest) Add(root string, file string) error {
	return gitError(g.RunCmdInDir(root, "add", file))
}
func (g *gitTest) AddAll(root string) error {
	return gitError(g.RunCmdInDir(root, "add", "."))
}
func (g *gitTest) Commit(root, msg string) error {
	return gitError(g.RunCmdInDir(root, "commit", "-m", msg))
}
func (g *gitTest) PopulateNonCasRepo(root string) error {
	// Create a git repo outside of cas to drive cas tests
	if err := g.InitEmpty(root); err != nil {
		return err
	}
	if err := PopulateTestStage(root); err != nil {
		return err
	}
	if err := g.AddAll(root); err != nil {
		return err
	}
	if err := g.Commit(root, "Setup reference git repo"); err != nil {
		return err
	}
	return nil
}
func (g *gitTest) CreateNonCasRepo(root string) error {
	if err := MkdirTest(root); err != nil {
		return fmt.Errorf("failed to create starting dir: %v", err)
	}
	return g.PopulateNonCasRepo(root)
}
func (g *gitTest) GetHead(root string) (string, error) {
	stdout, stderr, err := g.RunCmdInDir(root, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse: error %v (%s)", err, strings.Join(stderr, " "))
	}
	if len(stdout) != 1 {
		return "", fmt.Errorf("expected 1 line got %d lines", len(stdout))
	}
	return stdout[0], nil
}

func (g *gitTest) AllBlobs(root string) ([]string, error) {
	cmd := exec.Command("bash", "-c", "git rev-list --objects --all | git cat-file --batch-check='%(objectname) %(objecttype) %(rest)'")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	blobs := []string{}
	scanner := bufio.NewScanner(bytes.NewBuffer(output))
	for scanner.Scan() {
		line := scanner.Text()
		splits := strings.Split(line, " ")
		if len(splits) < 2 {
			return nil, fmt.Errorf("badly formatted line: %v", line)
		}
		if splits[1] == "blob" {
			blobs = append(blobs, splits[0])
		}
	}

	return blobs, nil
}

func (g *gitTest) GitShow(root string) ([]string, error) {
	stdout, stderr, err := g.RunCmdInDir(root, "show")
	if err != nil {
		return nil, gitError(stdout, stderr, err)
	}
	return stdout, err
}
func (g *gitTest) GitStatus(root string) ([]string, error) {
	stdout, stderr, err := g.RunCmdInDir(root, "status", "--short")
	if err != nil {
		return nil, gitError(stdout, stderr, err)
	}
	return stdout, err

}
func (g *gitTest) GitCommit(root, message string) ([]string, error) {
	stdout, stderr, err := g.RunCmdInDir(root, "commit", "-m", message)
	if err != nil {
		return nil, gitError(stdout, stderr, err)
	}
	return stdout, err
}
func (g *gitTest) CtrlGitTest(root, head string) error {
	if err := MkdirTest(root); err != nil {
		return err
	}
	return g.fs.CreateGit(root, ".git", head)

}

func (g *gitTest) CtrlGitRmTest(root, head string) error {
	if err := g.CtrlGitTest(root, head); err != nil {
		return err
	}
	path := filepath.Join(root, ".git")
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("unexpected stat after delete on %s", path)
	}

	return nil

}
func (g *gitTest) CtrlGitObjTest(parent, dirName string) error {
	if err := MkdirTest(parent); err != nil {
		return err
	}
	if err := g.fs.CreateGitObjects(parent, dirName); err != nil {
		return err
	}
	return ValidateDir(filepath.Join(parent, dirName))
}
func (g *gitTest) CtrlGitObjRmTest(parent, dirName string) error {
	if err := g.CtrlGitObjTest(parent, dirName); err != nil {
		return err
	}
	path := filepath.Join(parent, dirName)
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("unexpected stat after delete on %s", path)
	}
	return nil
}
func (g *gitTest) HeadTest(root, head string) error {
	if err := g.CtrlGitTest(root, head); err != nil {
		return err
	}
	back, err := g.GetHead(root)
	if err != nil {
		return err
	}
	if back != head {
		return fmt.Errorf("unexpected head %s expected %s", back, head)
	}
	return nil
}
func (g *gitTest) ShowTest(root, head string) error {
	if err := g.CtrlGitTest(root, head); err != nil {
		return err
	}
	_, err := g.GitShow(root)
	return err

}
func (g *gitTest) CommitTest(root, head string) error {
	if err := g.CtrlGitTest(root, head); err != nil {
		return err
	}
	start, err := g.GetHead(root)
	if err != nil {
		return fmt.Errorf("failed go get head: %v", err)
	}
	_, err = g.GitShow(root)
	if err != nil {
		return fmt.Errorf("failed to run show: %v", err)
	}
	filepath := path.Join(root, "test-file")
	if err := FileTestContent(filepath, "with data"); err != nil {
		return fmt.Errorf("failed to write test content: %v", err)
	}
	// Add file
	if err := g.Add(root, filepath); err != nil {
		return fmt.Errorf("failed to add file: %v", err)
	}
	if _, err := g.GitCommit(root, "commit test message"); err != nil {
		return fmt.Errorf("failed to commit: %v", err)
	}
	back, err := g.GetHead(root)
	if err != nil {
		return fmt.Errorf("failed to get new head: %v", err)
	}
	if start == back {
		return fmt.Errorf("expected new head got same %s as previous head", start)
	}

	return nil
}

func CopyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	dstFile, err := os.Create(dst)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer dstFile.Close()
	_, err = io.Copy(dstFile, srcFile)
	return err
}
func TxObjects(rootDir, cacheDir string) error {
	// Copy all objects from git dir to cache dir
	// inside cache dir create objects as xxx/xxx...
	gitObjDir := filepath.Join(rootDir, ".git", "objects")
	err := filepath.Walk(gitObjDir, func(path string, info iofs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, found := strings.CutPrefix(path, gitObjDir)
		if !found {
			return nil
		}
		name := strings.ReplaceAll(relPath, "/", "")
		if len(name) == 40 {
			dir := name[0:3]
			fname := name[3:]
			if err := os.MkdirAll(filepath.Join(cacheDir, dir), 0755); err != nil {
				return err
			}
			if err := CopyFile(path, filepath.Join(cacheDir, dir, fname)); err != nil {
				return err
			}

		}
		return nil
	})
	return err
}

func (g *gitTest) LFSLikeAccess(root, head string) error {
	if err := g.CtrlGitTest(root, head); err != nil {
		return err
	}
	blobs, err := g.AllBlobs(root)
	if err != nil {
		return err
	}

	readFile := func(path string) error {
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open error: %v", err)
		}
		defer f.Close()

		// git-lfs reads 4096 at a time
		buffer := make([]byte, 4096)

		_, err = f.Read(buffer)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read error: %v", err)
		}

		return nil
	}

	var eg errgroup.Group
	// Read all blobs multiple times from .git/objects
	// This simulates a LFS access pattern in a directory of files
	// where multiple files contain the same content and therefore same SHA
	for _, blob := range blobs {
		path := filepath.Join(root, ".git", "objects", blob[0:2], blob[2:])
		for i := 0; i < 10; i++ {
			eg.Go(func() error {
				return readFile(path)
			})
		}
	}

	return eg.Wait()
}

// MockTreeDir implements gitindex.TreeDirectory for testing
type MockTreeDir struct {
	DirName  string
	DirSha   [20]byte
	FileList []gitindex.FileEntry
	SubDirs  []gitindex.TreeDirectory
}

func (m *MockTreeDir) Name() string  { return m.DirName }
func (m *MockTreeDir) Sha() [20]byte { return m.DirSha }

func (m *MockTreeDir) Files(ctx context.Context) ([]gitindex.FileEntry, error) {
	return m.FileList, nil
}

func (m *MockTreeDir) Subtrees(ctx context.Context) ([]gitindex.TreeDirectory, error) {
	return m.SubDirs, nil
}

// CreateStageRepo creates a non-CAS git repo with the given files, commits it,
// copies all objects to the CAS object store, and returns the HEAD hash and a hierarchical TreeDirectory.
func (g *gitTest) CreateStageRepo(stagePath, objectsDir string, files map[string]string) (string, *MockTreeDir, error) {
	if err := g.InitEmpty(stagePath); err != nil {
		return "", nil, err
	}

	type dirBuilder struct {
		name    string
		files   []gitindex.FileEntry
		subdirs map[string]*dirBuilder
	}

	newDirBuilder := func(name string) *dirBuilder {
		return &dirBuilder{
			name:    name,
			files:   make([]gitindex.FileEntry, 0),
			subdirs: make(map[string]*dirBuilder),
		}
	}

	rootBuilder := newDirBuilder("")

	for relPath, content := range files {
		fullPath := filepath.Join(stagePath, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return "", nil, err
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			return "", nil, err
		}
		if err := g.Add(stagePath, relPath); err != nil {
			return "", nil, err
		}

		// Calculate Git blob SHA
		header := fmt.Sprintf("blob %d\x00", len(content))
		h := sha1.New()
		h.Write([]byte(header))
		h.Write([]byte(content))
		var sha [20]byte
		copy(sha[:], h.Sum(nil))

		// Navigate or create intermediate directories
		normalized := filepath.ToSlash(relPath)
		parts := strings.Split(normalized, "/")
		curr := rootBuilder
		for _, part := range parts[:len(parts)-1] {
			if _, ok := curr.subdirs[part]; !ok {
				curr.subdirs[part] = newDirBuilder(part)
			}
			curr = curr.subdirs[part]
		}

		// Store only the base name at the leaf directory level
		baseName := parts[len(parts)-1]
		curr.files = append(curr.files, gitindex.FileEntry{
			Name: baseName,
			Sha:  sha,
			Mode: 0100644,
			Size: uint32(len(content)),
		})
	}

	if err := g.Commit(stagePath, "Initial stage commit"); err != nil {
		return "", nil, err
	}

	head, err := g.GetHead(stagePath)
	if err != nil {
		return "", nil, err
	}

	// Copy the generated git objects into the CAS mount objects dir
	if err := TxObjects(stagePath, objectsDir); err != nil {
		return "", nil, fmt.Errorf("failed to tx objects: %w", err)
	}

	// Recursively build the MockTreeDir hierarchy
	var buildMockDir func(b *dirBuilder) *MockTreeDir
	buildMockDir = func(b *dirBuilder) *MockTreeDir {
		// Match the TreeDirectory contract: map iteration is randomized, so
		// sort explicitly or the mock emits an order the real DeepTree never
		// would.
		sort.Slice(b.files, func(i, j int) bool { return b.files[i].Name < b.files[j].Name })

		names := make([]string, 0, len(b.subdirs))
		for n := range b.subdirs {
			names = append(names, n)
		}
		sort.Strings(names)

		res := &MockTreeDir{
			DirName:  b.name,
			DirSha:   [20]byte{0x01}, // Non-zero mock sha for valid tree
			FileList: b.files,
			SubDirs:  make([]gitindex.TreeDirectory, 0, len(b.subdirs)),
		}
		for _, n := range names {
			res.SubDirs = append(res.SubDirs, buildMockDir(b.subdirs[n]))
		}
		return res
	}

	return head, buildMockDir(rootBuilder), nil
}

// SetupGitRepoForTreeIndex initializes .git structure without creating an index
func (g *gitTest) SetupGitRepoForTreeIndex(root, head string) error {
	gitPath := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitPath, 0755); err != nil {
		return fmt.Errorf("failed to create base git directory: %w", err)
	}

	if err := g.fs.SetExcluded(gitPath, true); err != nil {
		return fmt.Errorf("failed to set git to excluded: %w", err)
	}

	if err := g.fs.CreateGitObjects(gitPath, "objects"); err != nil {
		return fmt.Errorf("failed to create objects dir: %w", err)
	}

	for _, d := range []string{"objects/info", "objects/pack", "refs", "refs/heads", "refs/tags"} {
		if err := os.MkdirAll(filepath.Join(gitPath, d), 0755); err != nil {
			return err
		}
	}

	if head != "" {
		if err := os.WriteFile(filepath.Join(gitPath, "HEAD"), []byte(head+"\n"), 0644); err != nil {
			return err
		}
	}

	gitConfig := `[core]
	compression = 0
	looseCompression = 0
	abbrev = no
[fetch]
	unpackLimit = 2147483647
[maintenance.loose-objects]
	auto = 0
[maintenance.incremental-repack]
	auto = 0
[casfs]
	noObjectCompress = true
	flatObjectPath = true
`
	if err := os.WriteFile(filepath.Join(gitPath, "config"), []byte(gitConfig), 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// TreeIndexTest initializes the repo and builds the index from the TreeDirectory.
func (g *gitTest) TreeIndexTest(root string, treeDir gitindex.TreeDirectory, head string) ([]string, error) {
	if err := g.SetupGitRepoForTreeIndex(root, head); err != nil {
		return nil, err
	}

	if err := g.fs.CreateGitIndexFromTree(context.Background(), root, ".git", root, treeDir); err != nil {
		return nil, fmt.Errorf("failed to create git index from tree: %w", err)
	}

	return g.GitStatus(root)
}
