// Copyright 2025 Google LLC
// SPDX-License-Identifier: MIT
// See the LICENSE file in this directory for the full license text.

package cwalk

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
)

func TestSkipDir(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "cwalk-skipdir-test")
	if err != nil {
		t.Fatalf("Error creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a directory structure for testing
	// tmpDir/
	//   - dir1/
	//     - file1.txt
	//     - sub1/
	//       - file2.txt
	//   - dir2/
	//     - file3.txt
	//   - skip_this_dir/
	//     - file4.txt
	//     - sub2/
	//       - file5.txt

	dirs := []string{
		"dir1",
		filepath.Join("dir1", "sub1"),
		"dir2",
		"skip_this_dir",
		filepath.Join("skip_this_dir", "sub2"),
	}
	for _, dir := range dirs {
		if err := os.Mkdir(filepath.Join(tmpDir, dir), 0755); err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}
	}

	files := []string{
		filepath.Join("dir1", "file1.txt"),
		filepath.Join("dir1", "sub1", "file2.txt"),
		filepath.Join("dir2", "file3.txt"),
		filepath.Join("skip_this_dir", "file4.txt"),
		filepath.Join("skip_this_dir", "sub2", "file5.txt"),
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, file), []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create file %s: %v", file, err)
		}
	}

	var visitedPaths []string
	var mu sync.Mutex

	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if err != nil {
			return err
		}

		mu.Lock()
		visitedPaths = append(visitedPaths, path)
		mu.Unlock()

		if info.IsDir() && info.Name() == "skip_this_dir" {
			return filepath.SkipDir
		}
		return nil
	}

	if err := Walk(tmpDir, walkFn); err != nil {
		t.Fatalf("Walk returned an error: %v", err)
	}

	expectedPaths := []string{
		".",
		"dir1",
		"dir1/file1.txt",
		"dir1/sub1",
		"dir1/sub1/file2.txt",
		"dir2",
		"dir2/file3.txt",
		"skip_this_dir",
	}

	sort.Strings(visitedPaths)
	sort.Strings(expectedPaths)

	if !reflect.DeepEqual(visitedPaths, expectedPaths) {
		t.Errorf("Visited paths do not match expected paths.\nExpected: %v\nGot: %v", expectedPaths, visitedPaths)
	}
}
