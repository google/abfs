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

package entities

import (
	"cmp"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	PatchStatOperation FileMode = 01000000
	SrcDir                      = "/src"
)

type PatchEntryState int

const (
	// Default state before the state evaluation
	PatchEntryStateUnassigned PatchEntryState = 0
	// The patch is associated with a directory but the directory is modified during the build step
	PatchEntryStateDirModified PatchEntryState = 1
	// The patch is associated with a directory
	PatchEntryStateDir PatchEntryState = 2

	// Read states:
	//   Declared input in ninja and is read by the build process
	PatchEntryStateIn PatchEntryState = 3
	//   Found in the dep-file (.d file) and is read by the build process
	PatchEntryStateDep PatchEntryState = 4
	//   Declared as RSP file and is read by the build process
	PatchEntryStateRSP PatchEntryState = 5
	//   Declared input in ninja, but the build process has not read it
	PatchEntryStateExtraIn PatchEntryState = 6
	//   Not declared in the ninja, but the build process has read it (hidden dependency)
	PatchEntryStateExtraRead PatchEntryState = 7
	//   Declared as RSP file, but the build process has not read it
	PatchEntryStateNotReadRSP PatchEntryState = 8
	//   Found in the dep-file (.d file), but the build process has not read it
	PatchEntryStateNotReadDep PatchEntryState = 9
	//   Declared as Order-only input and the build process has read it
	PatchEntryStateOrderOnly PatchEntryState = 20
	//   Declared as Order-only input and but the build process has not read it
	PatchEntryStateNotReadOrderOnly PatchEntryState = 21
	//   Found read file that was not committed, file was auto snapshot
	PatchEntryInfoAutoSnapshot PatchEntryState = 22
	// Write states:
	//   Declared as a target in ninja and is written by the build process
	PatchEntryStateOut PatchEntryState = 10
	//   Declared as a depfile in ninja and is written by the build process
	PatchEntryStateDepFile PatchEntryState = 11
	//   Not declared in the ninja, but is written by the build process
	PatchEntryStateExtraWrite PatchEntryState = 12
	//   Declared as a depfile in ninja, but build process has not written it
	PatchEntryStateMissingDepFile PatchEntryState = 13
	//   Declared as a target in ninja, but build process has not written it
	PatchEntryStateMissingOut PatchEntryState = 14
)

// Const patch entry names
var PatchEntryMapName = map[PatchEntryState]string{
	PatchEntryStateUnassigned:       "unassigned",
	PatchEntryStateDirModified:      "dir-modified",
	PatchEntryStateDir:              "dir",
	PatchEntryStateIn:               "input",
	PatchEntryStateDep:              "dep",
	PatchEntryStateRSP:              "rsp",
	PatchEntryStateExtraIn:          "extra-input",
	PatchEntryStateExtraRead:        "hidden",
	PatchEntryStateNotReadRSP:       "not-read-rsp",
	PatchEntryStateNotReadDep:       "not-read-dep",
	PatchEntryStateOrderOnly:        "order-only",
	PatchEntryStateNotReadOrderOnly: "not-read-order-only",
	PatchEntryInfoAutoSnapshot:      "auto-snapshot",
	PatchEntryStateOut:              "target",
	PatchEntryStateDepFile:          "depfile",
	PatchEntryStateExtraWrite:       "extra-write",
	PatchEntryStateMissingDepFile:   "missing-depfile",
	PatchEntryStateMissingOut:       "missing-target",
}

func (s PatchEntryState) String() string {
	if n, ok := PatchEntryMapName[s]; ok {
		return n
	}
	return fmt.Sprintf("unknown state: %d", s)
}

func (s PatchEntryState) IsOrderOnly() bool {
	return s == PatchEntryStateOrderOnly || s == PatchEntryStateNotReadOrderOnly
}

// IsStateSetByTap identifies the "low-level" states that tap sets and shouldn't be overwritten
func (s PatchEntryState) IsStateSetByTap() bool {
	return s == PatchEntryInfoAutoSnapshot
}

func PatchEntryStateByName(s string) (PatchEntryState, error) {
	for v, k := range PatchEntryMapName {
		if k == s {
			return v, nil
		}
	}
	return PatchEntryStateUnassigned, fmt.Errorf("unknown state %s", s)
}

type PatchEntry struct {
	Path  string
	Mode  FileMode
	Hash  Hash
	Size  int64
	State PatchEntryState
}

func (p *PatchEntry) SetState(s PatchEntryState) {

	if p.State.IsStateSetByTap() {
		return
	}
	p.State = s
}

type Patch []*PatchEntry
type DeletePatch []string

func parentDir(p string) string {
	dir, _ := path.Split(p)
	return dir
}

func PathDiff(a, b string) string {
	a = strings.TrimPrefix(a, "/")
	b = strings.TrimPrefix(b, "/")
	if a == "" {
		return b
	}
	if b == "" {
		return "/"
	}

	p := parentDir(a)
	if strings.HasPrefix(b, p) {
		return b[len(p):]
	}
	r, err := filepath.Rel(p, b)
	if err != nil || len(r) > len(b) {
		return "/" + b
	}
	return r
}

func PathApply(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	if b[0] == '/' {
		return b[1:]
	}
	p := strings.TrimSuffix(parentDir(a), "/")
	for strings.HasPrefix(b, "../") {
		b = b[3:]
		p = strings.TrimSuffix(parentDir(p), "/")
	}
	b = strings.TrimPrefix(b, "/")
	if p == "" {
		return b
	}
	return p + "/" + b
}

func (p Patch) MarshalJSON() ([]byte, error) {
	p.sort()

	result := make([][]string, len(p))
	prev := ""
	hashes := make([]byte, len(p)*HashHexSize)
	for i, pi := range p {
		_ = hex.Encode(hashes[i*HashHexSize:(i+1)*HashHexSize], pi.Hash[:])
	}
	hashesStr := string(hashes)
	for i, pi := range p {
		result[i] = []string{
			PathDiff(prev, pi.Path),
			pi.Mode.ToLetter(),
			hashesStr[i*HashHexSize : (i+1)*HashHexSize],
			fmt.Sprintf("%d", pi.Size),
			strconv.Itoa(int(pi.State))}
		prev = pi.Path
	}
	return json.Marshal(result)
}

const patchFields = 5

func (p *Patch) UnmarshalJSON(data []byte) error {
	var result [][]string
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	pp := make(Patch, len(result))
	prev := ""
	for i, s := range result {
		if len(s) != patchFields {
			return fmt.Errorf("expected %d fields in %v", patchFields, s)
		}
		prev = PathApply(prev, s[0])
		mode, err := FileModeFromLetter(s[1])
		if err != nil {
			return fmt.Errorf("cannot parse mode %q: %w", s[1], err)
		}
		if !IsHash(s[2]) {
			return fmt.Errorf("cannot parse hash %q: invalid hash format", s[2])
		}
		size, err := strconv.ParseInt(s[3], 10, 64)
		if err != nil {
			return fmt.Errorf("cannot parse size %q: %w", s[3], err)
		}
		state, err := strconv.Atoi(s[4])
		if err != nil {
			return fmt.Errorf("cannot parse state %q: %w", s[4], err)
		}
		pp[i] = &PatchEntry{
			prev,
			mode,
			NewHash(s[2]),
			size,
			PatchEntryState(state),
		}
	}
	*p = pp
	return nil
}

// Transform paths to remove the input 'chroot'.
func ChrootTransform(path, chroot string) string {
	chroot = strings.Trim(chroot, "/")
	path = strings.TrimPrefix(path, "/")

	// Match path as chroot.
	if path == chroot || path == chroot+"/" {
		return ""
	}

	// Match paths starting with chroot/.
	chroot += "/"
	if path, found := strings.CutPrefix(path, chroot); found {
		return strings.TrimPrefix(path, "/")
	}

	// Match paths relative to the chroot.
	relPath, err := filepath.Rel(chroot, path)
	if err != nil {
		return path
	}

	return strings.TrimPrefix(relPath, "/")
}

func (p Patch) ChrootTransform(chroot string) {
	for _, pe := range p {
		pe.Path = ChrootTransform(pe.Path, chroot)
	}
}

func (p Patch) HashForPath(path string) (h Hash) {
	for _, i := range p {
		if i.Path == path {
			return i.Hash
		}
	}
	return h
}

func (p Patch) HashStatForPath(path string) (h HashStat) {
	for _, i := range p {
		if i.Path == path {
			return HashStat{
				Hash: i.Hash,
				Mode: i.Mode,
				Size: i.Size,
			}
		}
	}
	return h
}

// comparePath compares two paths as if each had a trailing slash ("/") appended,
// without allocating memory.
func comparePath(a, b string) int {
	// Effectively achieves the same as cmp.Compare(a+"/", b+"/") but without
	// allocating memory.
	n := min(len(a), len(b))
	if c := cmp.Compare(a[:n], b[:n]); c != 0 {
		return c
	}
	switch {
	case len(a) < len(b):
		if b[n] < '/' {
			return 1
		}
		return -1
	case len(a) > len(b):
		if a[n] < '/' {
			return -1
		}
		return 1
	default:
		return 0
	}
}

// sort is private because it's called during `MarshalJSON` so callers don't need to sort.
func (p Patch) sort() {
	slices.SortFunc(p, func(a, b *PatchEntry) int {
		return comparePath(a.Path, b.Path)
	})
}

func (p DeletePatch) MarshalJSON() ([]byte, error) {
	p.sort()

	result := make([]string, len(p))
	prev := ""
	for i, pi := range p {
		result[i] = PathDiff(prev, pi)
		prev = pi
	}
	return json.Marshal(result)
}

func (p *DeletePatch) UnmarshalJSON(data []byte) error {
	var result []string

	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	prev := ""
	for i, s := range result {
		prev = PathApply(prev, s)
		result[i] = prev
	}
	*p = result
	return nil
}

func (p DeletePatch) ChrootTransform(chroot string) {
	for i, s := range p {
		p[i] = ChrootTransform(s, chroot)
	}
}

// sort is private because it's called during `MarshalJSON` so callers don't need to sort.
func (p DeletePatch) sort() {
	slices.SortFunc(p, func(a, b string) int {
		return strings.Compare(a+"/", b+"/")
	})
}
