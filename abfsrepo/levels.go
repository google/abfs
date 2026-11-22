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

package abfsrepo

import (
	"path"
	"slices"
	"strings"
)

// PathLevels splits project paths into levels that are checked out one after another, as repo
// does: a path inside another path comes in a later level than that path, so a nested project
// is checked out into the work tree of the project around it. Each level is sorted. Paths are
// compared after path.Clean, and returned as given.
func PathLevels(paths []string) [][]string {
	sorted := slices.Clone(paths)
	// Sort by path element, so that a, a/b and a-b sort as a, a/b, a-b and a/b follows a.
	slices.SortFunc(sorted, func(a, b string) int {
		return slices.Compare(strings.Split(path.Clean(a), "/"), strings.Split(path.Clean(b), "/"))
	})

	type outer struct {
		path  string
		level int
	}
	var levels [][]string
	var stack []outer
	for _, p := range sorted {
		clean := path.Clean(p)
		for len(stack) > 0 && !strings.HasPrefix(clean, stack[len(stack)-1].path+"/") {
			stack = stack[:len(stack)-1]
		}
		level := 0
		if len(stack) > 0 {
			level = stack[len(stack)-1].level + 1
		}
		if level == len(levels) {
			levels = append(levels, nil)
		}
		levels[level] = append(levels[level], p)
		stack = append(stack, outer{path: clean, level: level})
	}
	return levels
}
