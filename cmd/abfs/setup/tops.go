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

package setup

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/abfs"
	"github.com/google/abfs/abfsrepo"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/manifest"
	"github.com/google/abfs/ninjago"
	"github.com/google/abfs/shared/entities"
)

// A Soong top is a directory that Soong itself accepts as the root of an Android tree: soong_ui
// needs build/soong/root.bp in its working directory, and ABFS mode needs the prebuilt tools
// directory below it. A repo workspace can hold any number of them at any depth.
const soongRootFile = "build/soong/root.bp"

// candidateTops returns the directories that may be Soong tops in a tree with the given project
// paths: the directories that hold a build/soong, build/make or build project.
func candidateTops(projectPaths []string) []string {
	seen := map[string]bool{}
	var tops []string
	add := func(dir string) {
		if dir == "." {
			dir = ""
		}
		if !seen[dir] {
			seen[dir] = true
			tops = append(tops, dir)
		}
	}
	for _, p := range projectPaths {
		p = path.Clean(filepath.ToSlash(p))
		if path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
			continue
		}
		switch {
		case p == "build/soong" || strings.HasSuffix(p, "/build/soong"),
			p == "build/make" || strings.HasSuffix(p, "/build/make"):
			add(path.Dir(path.Dir(p)))
		case p == "build" || strings.HasSuffix(p, "/build"):
			add(path.Dir(p))
		}
	}
	sort.Strings(tops)
	return tops
}

// soongTopProblem returns what dir lacks to pass Soong's own test for the top of an Android
// tree, or "" when it passes.
func soongTopProblem(dir string) string {
	binDirPath, err := binDir()
	if err != nil {
		return err.Error()
	}
	var missing []string
	if st, err := os.Stat(filepath.Join(dir, soongRootFile)); err != nil || st.IsDir() {
		missing = append(missing, soongRootFile)
	}
	if st, err := os.Stat(filepath.Join(dir, binDirPath)); err != nil || !st.IsDir() {
		missing = append(missing, binDirPath+"/")
	}
	if len(missing) == 0 {
		return ""
	}
	return strings.Join(missing, " and ") + " missing"
}

// projectPaths returns the project paths of the repo workspace at root, relative to root: the
// projects `abfs repo sync` checked out, and the projects of a classic mount's pinned manifest.
func projectPaths(root string) ([]string, error) {
	var paths []string
	list, err := os.Open(filepath.Join(root, casgit.RepoDirName, abfsrepo.ProjectsListFile))
	switch {
	case err == nil:
		lines, err := readLines(list)
		if closeErr := list.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read the project list of %s: %w", root, err)
		}
		paths = append(paths, lines...)
	case !entities.IsNotExist(err):
		return nil, err
	}

	pinned, err := manifest.ParseFile(filepath.Join(root, casgit.RepoDirName, abfs.ABFSManifestsDir, abfs.PinnedManifestFileName))
	switch {
	case err == nil:
		for _, p := range pinned.Project {
			paths = append(paths, p.GetPath())
		}
	case !entities.IsNotExist(err):
		return nil, fmt.Errorf("failed to read the pinned manifest of %s: %w", root, err)
	}
	return paths, nil
}

func readLines(r io.Reader) ([]string, error) {
	var lines []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

// DetectTops returns the Soong tops of the repo workspace at root, relative to root ("" for root
// itself). Candidates come from the workspace's project list, so no directory is walked.
func DetectTops(root string) ([]string, error) {
	tops, _, err := detectTops(root)
	return tops, err
}

// rejectedTop is a candidate from a project path that failed Soong's test.
type rejectedTop struct {
	top, problem string
}

// detectTops tests root and the candidates from its project list. It returns the tops and the
// candidates that failed.
func detectTops(root string) ([]string, []rejectedTop, error) {
	paths, err := projectPaths(root)
	if err != nil {
		return nil, nil, err
	}
	candidates := candidateTops(paths)
	fromProjects := make(map[string]bool, len(candidates))
	for _, top := range candidates {
		fromProjects[top] = true
	}
	var tops []string
	var rejected []rejectedTop
	for _, top := range uniqueSorted(append([]string{""}, candidates...)) {
		problem := soongTopProblem(filepath.Join(root, top))
		switch {
		case problem == "":
			tops = append(tops, top)
		case fromProjects[top]:
			rejected = append(rejected, rejectedTop{top, problem})
		}
	}
	return tops, rejected, nil
}

// findTops returns the tops to set up, relative to the mount point, and where they came from.
// The --chroot directory replaces detection. Detection looks at the mount
// root and at a repo checkout below it that contains start, and falls back to the mount root
// when it finds no top, as setup did before it knew about tops.
func (c *setupCmd) findTops(start string) ([]string, string, error) {
	if c.Chroot != "" {
		top, err := chrootTop(c.Chroot)
		return []string{top}, "--chroot", err
	}

	tops, rejected, err := detectTops(c.mountPoint)
	if err != nil {
		return nil, "", err
	}
	if repoRoot, err := abfsrepo.FindRepoRoot(start); err == nil {
		rel, err := filepath.Rel(c.mountPoint, repoRoot)
		if err == nil && rel != "." && filepath.IsLocal(rel) {
			nested, nestedRejected, err := detectTops(repoRoot)
			if err != nil {
				return nil, "", err
			}
			for _, top := range nested {
				tops = append(tops, filepath.Join(rel, top))
			}
			for _, r := range nestedRejected {
				rejected = append(rejected, rejectedTop{filepath.Join(rel, r.top), r.problem})
			}
		}
	}
	tops = uniqueSorted(tops)
	if len(tops) == 0 {
		if len(rejected) > 0 {
			names := make([]string, len(rejected))
			for i, r := range rejected {
				names[i] = fmt.Sprintf("%s (%s)", displayTop(r.top), r.problem)
			}
			c.warnf("Warning: no Soong top found in %s, so setup uses the mount root; candidates: %s\n", c.mountPoint, strings.Join(names, ", "))
		}
		return []string{""}, "fallback to the mount root, no Soong top found", nil
	}
	return tops, "detection", nil
}

// chrootTop returns the --chroot directory relative to the mount point. The daemon's chroot
// request creates the directory, so it need not exist yet: abfs nsjail passes a new one.
func chrootTop(chroot string) (string, error) {
	rel := filepath.Clean(chroot)
	if rel == "." {
		return "", nil
	}
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("--chroot %q is outside the mount point", chroot)
	}
	return rel, nil
}

func (c *setupCmd) warnf(format string, a ...any) {
	w := c.errOut
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, format, a...)
}

func uniqueSorted(s []string) []string {
	sort.Strings(s)
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// displayTop names a top for messages.
func displayTop(top string) string {
	if top == "" {
		return "."
	}
	return top
}

// setupBinaries are the files setup writes into the prebuilt tools directory of a top.
var setupBinaries = []string{"abfs-wrap", "abfsbox", "ckati-wrap", "ninjago", "abfs-cmd-map", "abfs-bazel-daemon", "rbetap", "tap"}

// listTops prints the tops setup would set up and what is in place in each of them.
func (c *setupCmd) listTops(w io.Writer, tops []string, source string) error {
	binDirPath, err := binDir()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Soong tops in %s (%s):\n", c.mountPoint, source)
	for _, top := range tops {
		dir := filepath.Join(c.mountPoint, top)
		fmt.Fprintf(w, "%s\n", displayTop(top))
		if problem := soongTopProblem(dir); problem != "" {
			fmt.Fprintf(w, "  not a Soong top: %s\n", problem)
		}

		conf := "missing"
		if nc, err := ninjago.ReadConfig(filepath.Join(dir, ninjago.ConfigFile)); err == nil {
			conf = fmt.Sprintf("Chroot %q, CacheDir %q", nc.Chroot, nc.CacheDir)
		}
		fmt.Fprintf(w, "  %-28s %s\n", ninjago.ConfigFile, conf)
		fmt.Fprintf(w, "  %-28s %s\n", vendorSetupRelativePath, presence(filepath.Join(dir, vendorSetupRelativePath)))

		var missing []string
		for _, b := range setupBinaries {
			if _, err := os.Stat(filepath.Join(dir, binDirPath, b)); err != nil {
				missing = append(missing, b)
			}
		}
		bins := "all present"
		if len(missing) > 0 {
			bins = "missing " + strings.Join(missing, " ")
		}
		fmt.Fprintf(w, "  %-28s %s\n", binDirPath, bins)

		link := "missing"
		if target, err := os.Readlink(filepath.Join(dir, casgit.RepoDirName, "mount-details")); err == nil {
			link = "-> " + target
		}
		fmt.Fprintf(w, "  %-28s %s\n", ".repo/mount-details", link)

		wrapped := "not wrapped"
		if wrapper, err := readNSJailWrapper(filepath.Join(dir, nsjailRelativePath)); err == nil && wrapper != nil {
			wrapped = "wrapped"
		}
		fmt.Fprintf(w, "  %-28s %s\n", nsjailRelativePath, wrapped)

		if top != "" {
			socket := "not served"
			if conn, err := net.DialTimeout("unix", filepath.Join(dir, entities.ABFSSocketFileName), time.Second); err == nil {
				socket = "served"
				_ = conn.Close()
			}
			fmt.Fprintf(w, "  %-28s %s\n", entities.ABFSSocketFileName, socket)
		}
	}
	return nil
}

func presence(p string) string {
	if _, err := os.Stat(p); err != nil {
		return "missing"
	}
	return "present"
}
