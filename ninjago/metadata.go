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
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/abfs/binding"
)

// buildMetadata returns the build metadata of an Android build, or nil in the bootstrap
// phase, which runs before the Android graph exists.
func (c *ABFSBackend) buildMetadata() *binding.BuildMetadata {
	if path.Base(c.ninjaEntryFile) == "bootstrap.ninja" {
		return nil
	}
	return readBuildMetadata(c.outDir)
}

// readBuildMetadata reads the user name and build number soong_ui wrote under outDir. It
// returns nil when the files are missing, as in builds outside Android.
func readBuildMetadata(outDir string) *binding.BuildMetadata {
	number, err := os.ReadFile(filepath.Join(outDir, "soong/build_number.txt"))
	if err != nil {
		return nil
	}
	// soong_ui writes BUILD_NUMBER here, or BUILD_USERNAME when BUILD_NUMBER is not set.
	tag, err := os.ReadFile(filepath.Join(outDir, "file_name_tag.txt"))
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(outDir, "soong/soong.environment.available"))
	if err != nil {
		return nil
	}
	var env []struct{ Key, Value string }
	if err := json.Unmarshal(data, &env); err != nil {
		return nil
	}

	m := &binding.BuildMetadata{BuildNumber: strings.TrimSpace(string(number))}
	for _, v := range env {
		if v.Key == "BUILD_USERNAME" {
			m.Username = v.Value
			break
		}
	}
	m.Pinned = strings.TrimSpace(string(tag)) != m.Username
	return m
}
