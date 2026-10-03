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

package tap

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// When running a tap resolve we need to track the file transactions and build
// a corresponding tree that represents the state of the writes so that at the
// end of the build we can iterate over and read the HashStat.  This is needed
// for the rename directory case.

type WalkFunc = func(path string, mode fs.FileMode) error

type TapTrack interface {
	Add(path string, mode fs.FileMode)
	Remove(path string)
	Rename(path, newPath string, newMode fs.FileMode)
	Walk(fn WalkFunc) error
}

type tapTrack struct {
	root tapTrackNode
}

const defDirMode fs.FileMode = fs.FileMode(fs.ModeDir | 0777)

func NewTapTrack() TapTrack {
	return &tapTrack{
		root: *NewTapTrackNode("/", defDirMode, true),
	}
}

func (t *tapTrack) Walk(fn WalkFunc) error {
	return t.root.walk("", fn)
}

func (t *tapTrack) Add(path string, mode fs.FileMode) {
	dir, name := t.findParent(path)
	dir.children[name] = NewTapTrackNode(name, mode, false)
}
func (t *tapTrack) Remove(path string) {
	dir, name := t.findParent(path)
	delete(dir.children, name)
}
func (t *tapTrack) Rename(path, newPath string, newMode fs.FileMode) {
	dir, name := t.findParent(path)
	newDir, newName := t.findParent(newPath)

	if oldNode, exists := dir.children[name]; !exists {
		newDir.children[newName] = NewTapTrackNode(newName, newMode, false)
	} else {
		oldNode.mode = newMode
		oldNode.name = newName
		newDir.children[newName] = oldNode
		delete(dir.children, name)
	}
}

func (t *tapTrack) findParent(path string) (*tapTrackNode, string) {
	comps := splitPath(path)
	if len(comps) == 0 {
		return nil, ""
	}

	name := comps[len(comps)-1]
	comps = comps[:len(comps)-1]
	dir := &t.root

	for _, dirName := range comps {
		if cd, exists := dir.children[dirName]; exists {
			dir = cd
		} else {
			// Auto create parents as needed
			newDir := NewTapTrackNode(dirName, defDirMode, true)
			dir.children[dirName] = newDir
			dir = newDir
		}
	}
	return dir, name
}

type tapTrackNode struct {
	name     string
	mode     fs.FileMode
	isAuto   bool
	children map[string]*tapTrackNode
}

func NewTapTrackNode(name string, mode fs.FileMode, isAuto bool) *tapTrackNode {
	return &tapTrackNode{
		name:     name,
		mode:     mode,
		isAuto:   isAuto,
		children: make(map[string]*tapTrackNode),
	}
}

func (t *tapTrackNode) walk(parentPath string, fn WalkFunc) error {
	ourPath := filepath.Join(parentPath, t.name)
	if !t.isAuto {
		if err := fn(ourPath, t.mode); err != nil {
			return err
		}
	}

	for _, c := range t.children {
		if err := c.walk(ourPath, fn); err != nil {
			return err
		}
	}
	return nil
}

func splitPath(p string) []string {
	p = filepath.Clean(p)

	// After cleaning, paths like "." or "/" or "" should result in no components.
	if p == "." || p == "/" || p == "" {
		return nil
	}

	// For absolute paths, TrimPrefix prevents an empty string at the beginning of the slice.
	// For example, "/a/b" would split into ["", "a", "b"] otherwise.
	p = strings.TrimPrefix(p, string(filepath.Separator))

	return strings.Split(p, string(filepath.Separator))
}
