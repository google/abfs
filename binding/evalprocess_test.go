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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestProcessResp(t *testing.T) {
	tests := []struct {
		name   string
		req    *EvalRequest
		resp   *EvalResponse
		chroot string
		genCWD func(root string) string
		// Function to generate response based on root (tmpDir)
		genResp  func(root string) *EvalResponse
		setup    func(t *testing.T, root string)
		validate func(t *testing.T, resp *EvalResponse)
	}{
		{
			name: "Basic Match",
			req: &EvalRequest{
				Targets: []string{"out/target"},
				InputPatch: entities.Patch{
					&entities.PatchEntry{Path: "in/source"},
				},
			},
			resp: &EvalResponse{
				WritePatch: entities.Patch{
					&entities.PatchEntry{Path: "out/target"},
				},
				ReadPatch: entities.Patch{
					&entities.PatchEntry{Path: "in/source"},
				},
			},
			validate: func(t *testing.T, resp *EvalResponse) {
				assert.Equal(t, entities.PatchEntryStateOut, resp.WritePatch[0].State)
				assert.Equal(t, entities.PatchEntryStateIn, resp.ReadPatch[0].State)
				assert.Empty(t, resp.MissingWrites)
				assert.Empty(t, resp.MissingReads)
			},
		},
		{
			name:   "Chroot Stripping",
			chroot: "/tmp/chroot",
			req: &EvalRequest{
				Targets: []string{"out/target"},
			},
			resp: &EvalResponse{
				WritePatch: entities.Patch{
					&entities.PatchEntry{Path: "/tmp/chroot/out/target"},
				},
			},
			validate: func(t *testing.T, resp *EvalResponse) {
				assert.Equal(t, "out/target", resp.WritePatch[0].Path)
				assert.Equal(t, entities.PatchEntryStateOut, resp.WritePatch[0].State)
			},
		},
		{
			name: "Input Symlink Following",
			req: &EvalRequest{
				Targets: []string{"out/target"},
				InputPatch: entities.Patch{
					&entities.PatchEntry{Path: "in/source"},
				},
			},
			resp: &EvalResponse{
				WritePatch: entities.Patch{
					&entities.PatchEntry{Path: "out/target"},
				},
				ReadPatch: entities.Patch{
					&entities.PatchEntry{Path: "in/dest"},
				},
			},
			setup: func(t *testing.T, root string) {
				err := os.MkdirAll(filepath.Join(root, "in"), 0755)
				require.NoError(t, err)
				// Create target file
				err = os.WriteFile(filepath.Join(root, "in/dest"), []byte("content"), 0644)
				require.NoError(t, err)
				// Create a symlink in/source -> in/dest
				err = os.Symlink("dest", filepath.Join(root, "in/source"))
				require.NoError(t, err)
			},
			validate: func(t *testing.T, resp *EvalResponse) {
				assert.Equal(t, entities.PatchEntryStateOut, resp.WritePatch[0].State)
				assert.Equal(t, entities.PatchEntryStateIn, resp.ReadPatch[0].State)
				assert.Empty(t, resp.MissingWrites)
				assert.Empty(t, resp.MissingReads)
			},
		},
		{
			name: "Output Symlink Following",
			req: &EvalRequest{
				Targets: []string{"out/symlink"},
			},
			genResp: func(root string) *EvalResponse {
				return &EvalResponse{
					WritePatch: entities.Patch{
						// using absolute path to test chroot transform in symlink
						&entities.PatchEntry{Path: filepath.Join(root, "out/target")},
					},
				}
			},
			setup: func(t *testing.T, root string) {
				err := os.MkdirAll(filepath.Join(root, "out"), 0755)
				require.NoError(t, err)
				// Create target file
				err = os.WriteFile(filepath.Join(root, "out/target"), []byte("content"), 0644)
				require.NoError(t, err)
				// Create a symlink out/symlink -> out/target
				err = os.Symlink("target", filepath.Join(root, "out/symlink"))
				require.NoError(t, err)
			},
			validate: func(t *testing.T, resp *EvalResponse) {
				// The response path should be resolved to the target
				assert.Equal(t, "out/target", resp.WritePatch[0].Path)
				assert.Equal(t, entities.PatchEntryStateOut, resp.WritePatch[0].State)
			},
		},
		{
			name: "Output Symlink Following with CWD",
			genCWD: func(root string) string {
				return root
			},
			req: &EvalRequest{
				Targets: []string{"out/symlink"},
			},
			genResp: func(root string) *EvalResponse {
				return &EvalResponse{
					WritePatch: entities.Patch{
						&entities.PatchEntry{Path: filepath.Join(root, "out/target")},
					},
				}
			},
			setup: func(t *testing.T, root string) {
				err := os.MkdirAll(filepath.Join(root, "out"), 0755)
				require.NoError(t, err)
				err = os.WriteFile(filepath.Join(root, "out/target"), []byte("content"), 0644)
				require.NoError(t, err)
				err = os.Symlink("target", filepath.Join(root, "out/symlink"))
				require.NoError(t, err)
			},
			validate: func(t *testing.T, resp *EvalResponse) {
				assert.Equal(t, "out/target", resp.WritePatch[0].Path)
				assert.Equal(t, entities.PatchEntryStateOut, resp.WritePatch[0].State)
			},
		},
		{
			name: "Depfile Handling",
			req: &EvalRequest{
				DepFile: "out/dep.d",
				Targets: []string{"out/target"},
			},
			genResp: func(root string) *EvalResponse {
				return &EvalResponse{
					WritePatch: entities.Patch{
						&entities.PatchEntry{Path: filepath.Join(root, "out/target")},
						&entities.PatchEntry{Path: filepath.Join(root, "out/dep.d")},
					},
					ReadPatch: entities.Patch{
						&entities.PatchEntry{Path: filepath.Join(root, "in/dep_source")},
					},
				}
			},
			setup: func(t *testing.T, root string) {
				err := os.MkdirAll(filepath.Join(root, "out"), 0755)
				require.NoError(t, err)
				content := []byte("out/target: in/dep_source")
				err = os.WriteFile(filepath.Join(root, "out/dep.d"), content, 0644)
				require.NoError(t, err)
			},
			validate: func(t *testing.T, resp *EvalResponse) {
				assert.Equal(t, entities.PatchEntryStateOut, resp.WritePatch[0].State)
				assert.Equal(t, entities.PatchEntryStateDepFile, resp.WritePatch[1].State)
				assert.Equal(t, entities.PatchEntryStateDep, resp.ReadPatch[0].State)
			},
		},
		{
			// This is the use case that ARES and the `abfs eval` command use
			name: "CWD and chroot together for depfile",
			genCWD: func(root string) string {
				return filepath.Join(root, "chroot")
			},
			chroot: "chroot",
			req: &EvalRequest{
				DepFile: "out/dep.d",
				Targets: []string{"out/target"},
			},
			resp: &EvalResponse{
				WritePatch: entities.Patch{
					&entities.PatchEntry{Path: "chroot/out/target"},
					&entities.PatchEntry{Path: "chroot/out/dep.d"},
				},
				ReadPatch: entities.Patch{
					&entities.PatchEntry{Path: "chroot/in/dep_source"},
				},
			},
			setup: func(t *testing.T, root string) {
				err := os.MkdirAll(filepath.Join(root, "chroot/out"), 0755)
				require.NoError(t, err)
				content := []byte("out/target: in/dep_source")
				err = os.WriteFile(filepath.Join(root, "chroot/out/dep.d"), content, 0644)
				require.NoError(t, err)
			},
			validate: func(t *testing.T, resp *EvalResponse) {
				assert.Equal(t, "out/target", resp.WritePatch[0].Path)
				assert.Equal(t, entities.PatchEntryStateOut, resp.WritePatch[0].State)
				assert.Equal(t, "out/dep.d", resp.WritePatch[1].Path)
				assert.Equal(t, entities.PatchEntryStateDepFile, resp.WritePatch[1].State)
				assert.Equal(t, "in/dep_source", resp.ReadPatch[0].Path)
				assert.Equal(t, entities.PatchEntryStateDep, resp.ReadPatch[0].State)
			},
		},
		{
			// This is the use case that some partner builds use because they have a chroot from the mount perspective,
			// but ninjago executes within an NSJail where the chroot path doesn't exist
			name:   "chroot in response paths, but not in depfile paths",
			chroot: "chroot",
			req: &EvalRequest{
				DepFile: "out/dep.d",
				Targets: []string{"out/target"},
			},
			resp: &EvalResponse{
				WritePatch: entities.Patch{
					&entities.PatchEntry{Path: "chroot/out/target"},
					&entities.PatchEntry{Path: "chroot/out/dep.d"},
				},
				ReadPatch: entities.Patch{
					&entities.PatchEntry{Path: "chroot/in/dep_source"},
				},
			},
			setup: func(t *testing.T, root string) {
				err := os.MkdirAll(filepath.Join(root, "out"), 0755)
				require.NoError(t, err)
				content := []byte("out/target: in/dep_source")
				err = os.WriteFile(filepath.Join(root, "out/dep.d"), content, 0644)
				require.NoError(t, err)
			},
			validate: func(t *testing.T, resp *EvalResponse) {
				assert.Equal(t, "out/target", resp.WritePatch[0].Path)
				assert.Equal(t, entities.PatchEntryStateOut, resp.WritePatch[0].State)
				assert.Equal(t, "out/dep.d", resp.WritePatch[1].Path)
				assert.Equal(t, entities.PatchEntryStateDepFile, resp.WritePatch[1].State)
				assert.Equal(t, "in/dep_source", resp.ReadPatch[0].Path)
				assert.Equal(t, entities.PatchEntryStateDep, resp.ReadPatch[0].State)
			},
		},
		{
			name: "Missing and Extra Entries",
			req: &EvalRequest{
				Targets: []string{"out/missing"},
				InputPatch: entities.Patch{
					&entities.PatchEntry{Path: "in/missing"},
				},
			},
			resp: &EvalResponse{
				WritePatch: entities.Patch{
					&entities.PatchEntry{Path: "out/extra"},
				},
				ReadPatch: entities.Patch{
					&entities.PatchEntry{Path: "in/extra"},
				},
			},
			validate: func(t *testing.T, resp *EvalResponse) {
				assert.Equal(t, entities.PatchEntryStateExtraWrite, resp.WritePatch[0].State)
				assert.Equal(t, entities.PatchEntryStateExtraRead, resp.ReadPatch[0].State)

				assert.Contains(t, resp.MissingWrites, "out/missing")
				assert.Equal(t, entities.PatchEntryStateMissingOut, resp.MissingWrites["out/missing"])

				assert.Contains(t, resp.MissingReads, "in/missing")
				assert.Equal(t, entities.PatchEntryStateExtraIn, resp.MissingReads["in/missing"])
			},
		},
		{
			name: "Eval Response post-process timing",
			req:  &EvalRequest{},
			resp: &EvalResponse{},
			validate: func(t *testing.T, resp *EvalResponse) {
				assert.Greater(t, resp.RunInfo.PostProcessDuration, float64(0))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			// follow symlinks if tmpdir gave us a symlink
			tmpDir, err := filepath.EvalSymlinks(tmpDir)
			require.NoError(t, err)

			cwd := ""
			if tc.genCWD != nil {
				cwd = tc.genCWD(tmpDir)
			}
			// If cwd is empty, we chdir to tmpDir, otherwise we pass it as CWD.
			// With CWD arg, we chdir to a random other dir to ensure we aren't relying on process CWD.
			if cwd == "" {
				t.Chdir(tmpDir)
			} else {
				otherDir := t.TempDir()
				t.Chdir(otherDir)
			}

			if tc.setup != nil {
				tc.setup(t, tmpDir)
			}

			resp := tc.resp
			if tc.genResp != nil {
				resp = tc.genResp(tc.chroot)
			}

			ProcessResp(
				EvalPostProcess{
					Req:           tc.req,
					Resp:          resp,
					ChrootToStrip: tc.chroot,
					CWD:           cwd,
				})

			if tc.validate != nil {
				tc.validate(t, resp)
			}
		})
	}
}
