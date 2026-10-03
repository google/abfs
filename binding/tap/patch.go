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
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/google/abfs/shared/entities"
)

func resolveOutDir(s, outDir string) string {
	return strings.ReplaceAll(s, "$OUT_DIR", outDir)
}

// # Note on ignoring
// This file previously had tap functionality that would ignore a regex of files and exclude
// them from the resulting patch. It was decided a better flow to have tap accurately report
// what happened. Then ninjago can decide what to do with the information.

var autoListRe = "($OUT_DIR/release-config|$OUT_DIR/.module_paths/.*|$OUT_DIR/.path_interposer)$"

const defState = entities.PatchEntryStateUnassigned

type tapsPatch struct {
	reads      map[string]*entities.PatchEntry
	writes     map[string]*entities.HashStat
	overwrites map[string]entities.Hash
	deletes    map[string]struct{}
	tracker    TapTrack
	snap       *snapshot
	strict     bool
	outDir     string
	autoListRe *regexp.Regexp
	tapCounts  map[entities.TapType]uint64
}

func newTapsPatch(snap *snapshot, strict bool, outDir string) *tapsPatch {
	return &tapsPatch{
		reads:      make(map[string]*entities.PatchEntry),
		writes:     make(map[string]*entities.HashStat),
		overwrites: make(map[string]entities.Hash),
		deletes:    make(map[string]struct{}),
		tracker:    NewTapTrack(),
		snap:       snap,
		strict:     strict,
		outDir:     outDir,
		autoListRe: regexp.MustCompile(resolveOutDir(autoListRe, outDir)),
		tapCounts:  make(map[entities.TapType]uint64),
	}
}

func (s *tapsPatch) update(showRaw bool, taps chan *entities.TapFSEntry) (errs error) {
	for t := range taps {
		if showRaw {
			fmt.Fprintln(os.Stderr, t)
		}
		if err := s.process(t); err != nil {
			// Exit out after so that all errors can be logged
			errs = errors.Join(errs, fmt.Errorf("failed to add tap %+v (%s)", t, err))
		}
	}
	return
}

func (s *tapsPatch) markOverwrite(p string) {
	s.overwrites[p] = entities.Hash{}
}

func (s *tapsPatch) isOverwritten(p string) bool {
	for p != "" && p != "/" && p != "." {
		if _, ok := s.overwrites[p]; ok {
			return true
		}
		p = path.Dir(p)
	}
	return false
}

func (s *tapsPatch) shouldAutoSnapshot(p string) bool {
	return s.autoListRe.MatchString(p)
}
func addPatchEntry(patch map[string]*entities.PatchEntry, path string, mode entities.FileMode, hash entities.Hash, size int64, state entities.PatchEntryState) {
	pe, exist := patch[path]
	if !exist {
		pe = &entities.PatchEntry{
			Path:  path,
			Mode:  mode,
			Hash:  hash,
			Size:  size,
			State: state,
		}
		patch[path] = pe
	} else {
		// We want to keep the entry as stat only if old and new mode values are both stats.
		stat := pe.Mode.IsStat() && mode.IsStat()
		mode.UnsetStat()
		if stat {
			mode.SetStat()
		}
		pe.Mode = mode
		pe.Hash = hash
		pe.Size = size
		pe.SetState(state)
	}
}

func (s *tapsPatch) addWrite(path string, t *entities.TapFSEntry) {
	s.tracker.Add(path, t.Mode)
}

func (s *tapsPatch) addRead(path string, t *entities.TapFSEntry) error {
	if t.Dirty {
		autoSnapshot := true
		if s.strict {
			autoSnapshot = s.shouldAutoSnapshot(path)
		}
		if autoSnapshot {
			log.Printf("Auto snapshotting %s\n", path)
			if pe, err := s.snap.Snapshot(path); err != nil {
				return err
			} else {
				addPatchEntry(s.reads, path, pe.Mode, pe.Hash, pe.Size, entities.PatchEntryInfoAutoSnapshot)
				return nil
			}
		}
		return fmt.Errorf("refusing to include read patch for dirty file %s", path)
	}
	addPatchEntry(s.reads, path, entities.ModeToFileMode(t.Mode), t.Hash, t.Size, defState)
	return nil
}
func (s *tapsPatch) addDelete(path string) {
	s.deletes[path] = struct{}{}
	s.tracker.Remove(path)
}
func (s *tapsPatch) process(t *entities.TapFSEntry) error {

	if t.IsARI {
		return fmt.Errorf("error: %s is an ARI %s node", t.Path, t.ARI)
	}
	s.tapCounts[t.Type]++

	switch t.Type {

	case entities.TapFSMkdir, entities.TapFSSymlink, entities.TapFSOpenWrite:
		s.markOverwrite(t.Path)
		s.addWrite(t.Path, t)

	case entities.TapFSRename:
		s.markOverwrite(t.Path)

		s.tracker.Rename(t.AuxPath, t.Path, t.Mode)

		s.deletes[t.AuxPath] = struct{}{}

	case entities.TapFSLink:
		s.markOverwrite(t.Path)

		if !s.isOverwritten(t.AuxPath) {
			if err := s.addRead(t.AuxPath, t); err != nil {
				return err
			}
		}
		s.addWrite(t.Path, t)

	case entities.TapFSUnlink:
		s.markOverwrite(t.Path)
		s.addDelete(t.Path)

	case entities.TapFSOpenRead:
		if s.isOverwritten(t.Path) {
			return nil
		}
		s.markOverwrite(t.Path)

		if err := s.addRead(t.Path, t); err != nil {
			return err
		}

	case entities.TapFSReadDir:
		if s.isOverwritten(t.Path) {
			return nil
		}

		if err := s.addRead(t.Path, t); err != nil {
			return err
		}
		return nil

	case entities.TapFSSymlinkRead:
		if s.isOverwritten(t.Path) {
			return nil
		}
		s.markOverwrite(t.Path)

		if err := s.addRead(t.Path, t); err != nil {
			return err
		}

	case entities.TapFSStat:
		if s.isOverwritten(t.Path) {
			return nil
		}

		mode := entities.ModeToFileMode(t.Mode)
		mode.SetStat()

		addPatchEntry(s.reads, t.Path, mode, t.Hash, t.Size, defState)
		return nil
	case entities.TapFSAttrChange:
		if !s.isOverwritten(t.Path) {
			if err := s.addRead(t.Path, t); err != nil {
				return err
			}
		}
		s.markOverwrite(t.Path)
		s.addWrite(t.Path, t)
	default:
		return fmt.Errorf("unknown tap type %s", t.Type)
	}
	return nil
}
