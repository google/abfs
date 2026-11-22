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
	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/entities"
)

// missingTargets returns the targets that resp neither writes nor reads. Such a response does not
// say what the targets hold, so it cannot be applied.
func missingTargets(resp *binding.EvalResponse, targets []string) map[string]struct{} {
	targetsMap := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		targetsMap[t] = struct{}{}
	}
	for _, ent := range resp.WritePatch {
		delete(targetsMap, ent.Path)
	}
	if len(targetsMap) > 0 {
		for _, ent := range resp.ReadPatch {
			delete(targetsMap, ent.Path)
			if len(targetsMap) == 0 {
				break
			}
		}
	}
	return targetsMap
}

// readUnwrittenTargets adds the targets that a successful step which wrote nothing left on disk to
// the reads of resp, with their current content, as for a step that reads its targets. A
// placeholder step for a file that soong_ui writes before ninja runs, such as
// out/soong/build_number.txt, then hits in a fresh out dir while the file has the recorded content;
// each new content adds one response. Build metadata that ignoreMatch skips matches with any
// content. The target then shows as a read in eval output, abfs export and abfs diff.
func readUnwrittenTargets(resp *binding.EvalResponse, targets []string) error {
	return binding.AddUnwrittenTargets(resp, targets, func(t string) (*entities.PatchEntry, error) {
		hs, err := fileHashStat(t)
		if err != nil || hs == notExistsStat {
			return nil, err
		}
		ent := toPatchEntry(hs, t)
		return &ent, nil
	})
}
