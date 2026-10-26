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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
)

// writeBuildMetadata writes the files soong_ui writes for the build number, the file name
// tag and the environment; an empty value leaves the file out.
func writeBuildMetadata(t *testing.T, outDir, username, number, tag string) {
	t.Helper()
	soongDir := filepath.Join(outDir, "soong")
	require.NoError(t, os.MkdirAll(soongDir, 0755))
	if number != "" {
		require.NoError(t, os.WriteFile(filepath.Join(soongDir, "build_number.txt"), []byte(number), 0644))
	}
	if tag != "" {
		require.NoError(t, os.WriteFile(filepath.Join(outDir, "file_name_tag.txt"), []byte(tag), 0644))
	}
	if username != "" {
		env := `[
    {
        "Key": "BUILD_USERNAME",
        "Value": "` + username + `"
    },
    {
        "Key": "OUT_DIR",
        "Value": "out"
    }
]
`
		require.NoError(t, os.WriteFile(filepath.Join(soongDir, "soong.environment.available"), []byte(env), 0644))
	}
}

func TestReadBuildMetadata(t *testing.T) {
	tests := []struct {
		name     string
		username string
		number   string
		tag      string
		want     *binding.BuildMetadata
	}{
		{name: "no build number file", username: "builder", tag: "builder"},
		{name: "no environment file", number: "eng.builde", tag: "builder"},
		{name: "no file name tag", username: "builder", number: "eng.builde"},
		{
			name:     "BUILD_NUMBER unset",
			username: "builder",
			number:   "eng.builde",
			tag:      "builder",
			want:     &binding.BuildMetadata{Username: "builder", BuildNumber: "eng.builde"},
		},
		{
			name:     "ABFS pins",
			username: "abfs-builder",
			number:   "eng",
			tag:      "eng",
			want:     &binding.BuildMetadata{Username: "abfs-builder", BuildNumber: "eng", Pinned: true},
		},
		{
			name:     "BUILD_NUMBER set per job",
			username: "builder",
			number:   "1234",
			tag:      "1234",
			want:     &binding.BuildMetadata{Username: "builder", BuildNumber: "1234", Pinned: true},
		},
		{
			name:     "BUILD_USERNAME set, BUILD_NUMBER unset",
			username: "ci",
			number:   "eng.ci",
			tag:      "ci",
			want:     &binding.BuildMetadata{Username: "ci", BuildNumber: "eng.ci"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()
			writeBuildMetadata(t, outDir, tt.username, tt.number, tt.tag)
			require.Equal(t, tt.want, readBuildMetadata(outDir))
		})
	}
}

func TestBuildMetadata_SkipsBootstrap(t *testing.T) {
	tests := []struct {
		entryFile string
		wantNil   bool
	}{
		{entryFile: "out/soong/bootstrap.ninja", wantNil: true},
		{entryFile: "out/combined-test.ninja"},
	}

	for _, tt := range tests {
		t.Run(tt.entryFile, func(t *testing.T) {
			c := NewABFSBackend()
			c.outDir = t.TempDir()
			c.ninjaEntryFile = tt.entryFile
			writeBuildMetadata(t, c.outDir, "abfs-builder", "eng", "eng")
			require.Equal(t, tt.wantNil, c.buildMetadata() == nil)
		})
	}
}
