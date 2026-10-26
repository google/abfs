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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/entities"
)

const (
	buildDateFile   = "out/build_date.txt"
	buildNumberFile = "out/soong/build_number.txt"
)

var (
	hashA = entities.NewHash("1111111111111111111111111111111111111111")
	hashB = entities.NewHash("2222222222222222222222222222222222222222")
)

func TestIgnoreMatch_BuildMetadata(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		state entities.PatchEntryState
		want  bool
	}{
		{name: "undeclared build date read", path: buildDateFile, state: entities.PatchEntryStateExtraRead, want: true},
		{name: "declared build date", path: buildDateFile, state: entities.PatchEntryStateIn, want: true},
		{name: "order-only build date", path: buildDateFile, state: entities.PatchEntryStateOrderOnly, want: true},
		{name: "undeclared build number read", path: buildNumberFile, state: entities.PatchEntryStateExtraRead},
		{name: "declared source file", path: "src/a.txt", state: entities.PatchEntryStateIn},
	}

	c := NewABFSBackend()
	c.config = &Configuration{OrderOnlyAsRegularInput: true}
	c.matchFiles = newMatchFiles("out")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ent := &entities.PatchEntry{Path: tt.path, Mode: entities.FileModeRegular, Hash: hashA, State: tt.state}
			require.Equal(t, tt.want, c.ignoreMatch(ent))
		})
	}
}

func TestCleanMatch_BuildDateChanged(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "build date", path: buildDateFile},
		{name: "source file", path: "src/a.txt", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewABFSBackend()
			c.config = &Configuration{OrderOnlyAsRegularInput: true}
			c.matchFiles = newMatchFiles("out")
			c.hashes.Put(tt.path, &HashStat{Hash: hashB, Mode: entities.FileModeRegular})

			// The read was recorded with hash A; the file now has hash B.
			err := c.cleanMatch(&entities.PatchEntry{
				Path:  tt.path,
				Mode:  entities.FileModeRegular,
				Hash:  hashA,
				State: entities.PatchEntryStateExtraRead,
			})
			if tt.wantErr {
				require.True(t, binding.IsMatchError(err), "got %v", err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
