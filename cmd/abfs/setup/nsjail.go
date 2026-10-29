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
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/abfs/shared/entities"
)

// Soong runs its sandbox with this nsjail path. On some releases, when it uses ABFS, it passes
// the literal bind mount "-B {ABFS_DIR}", which nothing fills in, so nsjail cannot start. Later
// releases bind the cache directory from .repo/mount-details instead.
const (
	nsjailRelativePath       = buildToolsRelativeDir + "/linux-x86/bin/nsjail"
	soongSandboxRelativePath = "build/soong/ui/build/sandbox_linux.go"
	abfsDirPlaceholder       = "{ABFS_DIR}"
	nsjailWrapperHeader      = "#!/bin/bash\n# ABFS nsjail wrapper, installed by abfs setup.\n"
	nsjailExclude            = "/linux-x86/bin/nsjail.real\n"
)

func soongPassesABFSDir(root string) (bool, error) {
	content, err := os.ReadFile(filepath.Join(root, soongSandboxRelativePath))
	if entities.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return bytes.Contains(content, []byte(`"`+abfsDirPlaceholder+`"`)), nil
}

func nsjailWrapper(cacheDir string) []byte {
	// Soong runs nsjail with a minimal PATH, so the wrapper uses bash builtins only.
	return []byte(nsjailWrapperHeader + `# Replaces the ` + abfsDirPlaceholder + ` bind mount that Soong passes with the ABFS cache directory.
dir='` + strings.ReplaceAll(cacheDir, "'", `'\''`) + `'
args=()
for a in "$@"; do args+=("${a//\{ABFS_DIR\}/"$dir"}"); done
exec "${0%/*}/nsjail.real" "${args[@]}"
`)
}

// readNSJailWrapper returns the content of the nsjail at path if it is a wrapper written by
// wrapNSJail, and nil if it is not a script.
func readNSJailWrapper(path string) (_ []byte, retErr error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer entities.CloseErrCheck(f, &retErr)
	head := make([]byte, len(nsjailWrapperHeader))
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if string(head[:n]) != nsjailWrapperHeader {
		// Moving another wrapper aside would make the new one run itself.
		if bytes.HasPrefix(head[:n], []byte("#!")) {
			return nil, fmt.Errorf("%s is a script that abfs setup did not write; %s", path, restoreHint(path))
		}
		return nil, nil
	}
	rest, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return append(head, rest...), nil
}

func restoreHint(path string) string {
	return fmt.Sprintf("restore nsjail with: git -C %s checkout -- linux-x86/bin/nsjail", filepath.Dir(filepath.Dir(filepath.Dir(path))))
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if entities.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// wrapNSJail installs the nsjail wrapper in the tree at root when its Soong passes the
// placeholder, and puts the original nsjail back when it does not.
func wrapNSJail(root, cacheDir string) error {
	needed, err := soongPassesABFSDir(root)
	if err != nil {
		return err
	}
	if !needed {
		_, err := unwrapNSJail(root)
		return err
	}
	return installNSJailWrapper(root, nsjailWrapper(cacheDir))
}

func installNSJailWrapper(root string, wrapper []byte) error {
	path := filepath.Join(root, nsjailRelativePath)
	hasReal, err := exists(path + ".real")
	if err != nil {
		return err
	}
	current, err := readNSJailWrapper(path)
	switch {
	case entities.IsNotExist(err):
		// An earlier run may have stopped after moving the original aside.
		if !hasReal {
			log.Printf("Warning: %s not found, Soong cannot run its sandbox", path)
			return nil
		}
	case err != nil:
		return err
	case current == nil:
		if err := os.Rename(path, path+".real"); err != nil {
			return fmt.Errorf("failed to move %s aside: %w", path, err)
		}
	case !hasReal:
		return fmt.Errorf("%s.real is missing behind the ABFS nsjail wrapper; %s", path, restoreHint(path))
	}
	if err := os.WriteFile(path+".tmp", wrapper, 0755); err != nil {
		return fmt.Errorf("failed to write the nsjail wrapper: %w", err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return fmt.Errorf("failed to install the nsjail wrapper: %w", err)
	}
	return excludeNSJailReal(filepath.Join(root, buildToolsRelativeDir))
}

// unwrapNSJail puts the original nsjail back and returns the wrapper it replaced, or nil if
// nsjail was not wrapped.
func unwrapNSJail(root string) ([]byte, error) {
	path := filepath.Join(root, nsjailRelativePath)
	hasReal, err := exists(path + ".real")
	if err != nil {
		return nil, err
	}
	wrapper, err := readNSJailWrapper(path)
	if entities.IsNotExist(err) && hasReal {
		// An earlier run stopped after moving the original aside.
		if err := os.Rename(path+".real", path); err != nil {
			return nil, fmt.Errorf("failed to restore %s: %w", path, err)
		}
		return nil, nil
	}
	if entities.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || wrapper == nil {
		return nil, err
	}
	if !hasReal {
		return nil, fmt.Errorf("%s.real is missing behind the ABFS nsjail wrapper; %s", path, restoreHint(path))
	}
	if err := os.Rename(path+".real", path); err != nil {
		return nil, fmt.Errorf("failed to restore %s: %w", path, err)
	}
	return wrapper, nil
}

// excludeNSJailReal keeps the moved original out of `repo status`. The wrapper itself shows as
// a change, because git does not exclude tracked files.
func excludeNSJailReal(buildToolsDir string) (retErr error) {
	excludePath := filepath.Join(buildToolsDir, ".git", "info", "exclude")
	existing, err := os.ReadFile(excludePath)
	if err != nil && !entities.IsNotExist(err) {
		return err
	}
	if bytes.Contains(existing, []byte(nsjailExclude)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		return fmt.Errorf("failed to create git excludes directory %q: %w", filepath.Dir(excludePath), err)
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open git excludes file %q: %w", excludePath, err)
	}
	defer entities.CloseErrCheck(f, &retErr)
	line := nsjailExclude
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		line = "\n" + line
	}
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("failed to append to git excludes file %q: %w", excludePath, err)
	}
	return nil
}

// KeepNSJailWrapper runs sync with the original nsjail in place, because a checkout that
// changes nsjail fails while the wrapper replaces it, and installs the wrapper again after a
// successful sync if the tree still needs it.
func KeepNSJailWrapper(root string, sync func() error) error {
	wrapper, err := unwrapNSJail(root)
	if err != nil {
		return err
	}
	// After a failed sync the patched repo falls back to its own sync, which would stop at the
	// wrapper; abfs setup wraps nsjail again.
	if err := sync(); err != nil {
		return err
	}
	if wrapper == nil {
		return nil
	}
	needed, err := soongPassesABFSDir(root)
	if err != nil || !needed {
		return err
	}
	if err := installNSJailWrapper(root, wrapper); err != nil {
		return fmt.Errorf("failed to wrap nsjail again: %w", err)
	}
	return nil
}
