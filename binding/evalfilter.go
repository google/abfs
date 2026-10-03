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

package binding

import (
	"strings"

	"github.com/google/abfs/shared/entities"
)

var excludeOutDirListingExacts = map[string]bool{
	"":            true,
	"soong":       true,
	"soong/.temp": true,
}

// Currently, some Android build rules change their input directories. Rust in addition uses library
// directories that might later be mutated by other targets.
// These behaviors can lead to unnecessary mismatches, so we temporarily allowlist them. However, we
// should eventually fix the rules to prevent such changes, as they can cause non-deterministic
// build outcomes (non-hermicity).
var excludeOutDirListingPrefixes = []string{
	"soong/.intermediates",
	"target/common/obj/APPS",
	"target/common/obj/PACKAGING",
	"target/product",
}

type readfilter struct {
}

func NewReadPatchFilter() ReadPatchFilter {
	return &readfilter{}
}

func excludeDirListing(p string) bool {
	p = strings.TrimPrefix(p, entities.OutDir)
	p = strings.TrimPrefix(p, "/")
	if excludeOutDirListingExacts[p] {
		return true
	}

	if strings.Contains(p, "_rlib_") {
		return true
	}
	for _, prefix := range excludeOutDirListingPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	if strings.Contains(p, "/.git/") || strings.HasSuffix(p, "/.git") {
		return true
	}

	return false
}
func TransformReadPatchEntry(p *entities.PatchEntry, revalidate bool) *entities.PatchEntry {
	if revalidate {
		// Launching build will clear out sbox directory but this can be captured in
		// a patch, ignore for now.
		if p.Path == "out/soong/.temp/sbox" || strings.HasPrefix(p.Path, "out/soong/metrics_aggregation") {
			return nil
		}
	}
	if p.Mode == entities.FileModeDir {
		if revalidate || excludeDirListing(p.Path) || p.State == entities.PatchEntryStateDirModified {
			return &entities.PatchEntry{
				Path:  p.Path,
				Mode:  p.Mode,
				Hash:  entities.ZeroHash,
				Size:  p.Size,
				State: p.State}
		}
	}
	return p
}
func (r *readfilter) Filter(readPatch entities.Patch, revalidate bool) (entities.Patch, error) {
	result := make(entities.Patch, 0, len(readPatch))
	for _, p := range readPatch {
		n := TransformReadPatchEntry(p, revalidate)
		if n != nil {
			result = append(result, n)
		}
	}
	return result, nil
}
