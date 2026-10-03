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

package ninjago

import (
	"path"
	"strings"

	"github.com/google/ninja-go/build"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

type matchFiles struct {
	buildDate        string
	soongBuildDate   string
	soongTemp        string
	soongMetricsAggr string
	soongBuild       string
}

func newMatchFiles(outDir string) *matchFiles {
	return &matchFiles{
		buildDate:        path.Join(outDir, "build_date.txt"),
		soongBuildDate:   path.Join(outDir, "soong/build_date.txt"),
		soongTemp:        path.Join(outDir, "soong/.temp") + "/",
		soongMetricsAggr: path.Join(outDir, "soong/metrics_aggregation"),
		soongBuild:       path.Join(outDir, "soong/build"),
	}
}

// isOrderOnlyForcedHonor indicates that if the specified order-only path should be honored as
// order-only, regardless of the OrderOnlyAsRegularInput config.
func (c *ABFSBackend) isOrderOnlyForcedHonor(p string) bool {
	if strings.HasPrefix(p, c.matchFiles.buildDate) {
		return true
	}
	if strings.HasPrefix(p, c.matchFiles.soongBuildDate) {
		return true
	}
	return false
}

func (c *ABFSBackend) orderOnlyAsRegularInput(p string) bool {
	return c.config.OrderOnlyAsRegularInput && !c.isOrderOnlyForcedHonor(p)
}

// ignoreMatch is the proper place to ignore most problematic files. Some of this functionality previously
// existed in tap and would filter out files from the eval response. It was decided a better flow to
// have tap accurately report what happened. And the eval response stores what happened. Then when
// we are matching the read patch against the filesystem (here), we can decide whether to ignore any
// information.
func (c *ABFSBackend) ignoreMatch(ent *entities.PatchEntry) bool {
	// Ignore the order-only entries
	if ent.State.IsOrderOnly() && !c.orderOnlyAsRegularInput(ent.Path) {
		return true
	}

	// Launching build will clear out soong/.temp directory but this can be captured in a patch, ignore for now.
	// Some build steps also leave empty dirs around in it.
	if strings.HasPrefix(ent.Path, c.matchFiles.soongTemp) {
		return true
	}

	// soong_ui removes this directory after each before.
	if strings.HasPrefix(ent.Path, c.matchFiles.soongMetricsAggr) || strings.HasSuffix(ent.Path, "/soong_build_metrics.pb") {
		return true
	}

	// soong_ui rewrites this file on each build.
	if strings.HasPrefix(ent.Path, c.matchFiles.soongBuild) && strings.HasSuffix(ent.Path, ".ninja.globs_time") {
		return true
	}

	// handle out/.path_interposer_origpath
	if strings.HasSuffix(ent.Path, ".path_interposer_origpath") {
		return true
	}

	if c.config.IgnoreSoongEnvAvail {
		if strings.HasSuffix(ent.Path, "/soong/soong.environment.available") {
			return true
		}
	}

	if filesystem.IsGitPath(ent.Path) {
		return true
	}

	return false
}

func (c *ABFSBackend) cleanMatch(ent *entities.PatchEntry) error {
	if c.ignoreMatch(ent) {
		return nil
	}

	hs, err := c.lookup(ent.Path)
	if err != nil {
		return err
	}
	if hs == notExistsStat {
		return binding.NewMatchError("file %q does not exist", ent.Path)
	}

	if ent.Mode.IsStat() {
		return nil
	}

	if ent.Mode == entities.FileModeDir {
		// Check source directories.
		if !strings.HasPrefix(ent.Path, c.outDir) {
			p := ent.Path
			if ent.Path == "" {
				p = "."
			}

			// Read directory hash
			dh, err := bindcas.DirList(p)
			if err != nil {
				return err
			}
			if dh != ent.Hash {
				return binding.NewMatchError("dir %q list hash is %s, expected %s", p, dh, ent.Hash)
			}
		}
		return nil
	}

	// Ignore the potential stat flag and check modes
	mode := ent.Mode
	mode.UnsetStat()
	if mode != hs.Mode {
		return binding.NewMatchError("file %q mode is %s, expected %s", ent.Path, hs.Mode, ent.Mode)
	}

	// Check the hashes only if it is not a stat
	if !ent.Mode.IsStat() && ent.Hash != hs.Hash {
		return binding.NewMatchError("file %q hash is %s, expected %s", ent.Path, hs.Hash, ent.Hash)
	}
	return nil
}

func (c *ABFSBackend) cacheMatch(resp *binding.EvalResponse, eval *Eval) error {
	reduced, err := c.reduceMatches(resp, eval)
	if err != nil {
		return binding.NewMatchError("cache mismatch (from ninja) for %s req=%s resp=%s: %v", eval.step, eval.req.Hash, resp.Hash, err.Error())
	}

	err = c.abfs.ApplyPatch(reduced, resp.WritePatch, resp.Deletes, c.chroot)
	if !binding.IsMatchError(err) {
		return err
	}
	return binding.NewMatchError("cache mismatch (from fs) for %s req=%s resp=%s: %v", eval.step, eval.req.Hash, resp.Hash, err.Error())
}

// Currently, some Android build rules change their input directories. Rust in addition uses library
// directories that might later be mutated by other targets.
// These behaviors can lead to unnecessary mismatches, so we temporarily allow-list them. However, we
// should eventually fix the rules to prevent such changes, as they can cause non-deterministic
// build outcomes (non-hermicity).
var excludeOutDirListingExacts = map[string]bool{
	"":            true,
	"soong":       true,
	"soong/.temp": true,
}

func excludeDirListing(p string, eval *Eval) bool {
	if eval.step.Node.Build.RuleName == "g.rust.rustc" {
		return true
	}

	p = strings.TrimPrefix(p, entities.OutDir)
	p = strings.TrimPrefix(p, "/")
	if excludeOutDirListingExacts[p] {
		return true
	}

	if strings.HasPrefix(p, "soong/.intermediates/") {
		base := path.Base(p)
		if strings.Contains(base, "static") || strings.Contains(base, "shared") {
			return true
		}
	}

	if filesystem.IsGitPath(p) {
		return true
	}

	return false
}

func ignoreDirListForStep(step *build.Step) bool {
	t := step.Node.Targets[0]
	return strings.HasSuffix(t, ".rlib") || strings.HasSuffix(t, ".clippy")
}

func (c *ABFSBackend) reduceMatches(resp *binding.EvalResponse, eval *Eval) (entities.Patch, error) {
	ignoreDirList := ignoreDirListForStep(eval.step)

	patch := resp.ReadPatch
	reduced := make(entities.Patch, 0)
	add := func(ent *entities.PatchEntry) {
		reduced = append(reduced, ent)
	}
	for _, ent := range patch {
		if c.ignoreMatch(ent) {
			continue
		}

		if ent.State == entities.PatchEntryStateDirModified {
			ent.Hash = entities.ZeroHash
		}
		if ent.Mode == entities.FileModeDir && (ignoreDirList || excludeDirListing(ent.Path, eval)) {
			ent.Hash = entities.ZeroHash
		}

		if ent.Mode == entities.FileModeDir {
			add(ent)
			continue
		}

		hs := c.hashes.Get(ent.Path)
		if hs == nil {
			add(ent)
			continue
		}

		if hs == notExistsStat {
			return nil, binding.NewMatchError("file %q does not exist", ent.Path)
		}

		if ent.Mode.IsStat() {
			continue
		}

		// Ignore the stat and check modes
		mode := ent.Mode
		mode.UnsetStat()
		if mode != hs.Mode {
			return nil, binding.NewMatchError("file %q mode is %s, expected %s", ent.Path, hs.Mode, ent.Mode)
		}

		// Check the hashes only if it is not a stat
		if !ent.Mode.IsStat() && ent.Hash != hs.Hash {
			return nil, binding.NewMatchError("file %q hash is %s, expected %s", ent.Path, hs.Hash, ent.Hash)
		}
	}
	return reduced, nil
}
