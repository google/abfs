// Copyright 2025 Google LLC
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

package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

/*
 * Test a given CAS file system.
 *
 * Tests will check the hash value of files/directories/symlinks versus
 * empty/zero or that they have changed but not the actual hash values
 * these tests will be handled by the specific hasher tests
 *
 */
func TestCAS_Dir(t *testing.T) {
	path := env.CasTestNextPath()
	err := env.cas.MkdirTest(path)
	require.NoError(t, err)
}
func TestCAS_DirHash(t *testing.T) {
	path := env.CasTestNextPath()
	err := env.cas.MkdirTestStat(path)
	require.NoError(t, err)
}
func TestCAS_File(t *testing.T) {
	path := env.CasTestNextPath()
	err := env.cas.FileTestContent(path, "hash content")
	require.NoError(t, err)
}
func TestCAS_FileStat(t *testing.T) {
	path := env.CasTestNextPath()
	err := env.cas.FileTestContentStat(path, "hash content")
	require.NoError(t, err)
}
func TestCAS_FileTruncate(t *testing.T) {
	path := env.CasTestNextPath()
	err := env.cas.FileTestTruncate(path, "hash content")
	require.NoError(t, err)
}
func TestCAS_FileTruncateOpen(t *testing.T) {
	path := env.CasTestNextPath()
	err := env.cas.FileTestTruncateOpen(path, "hash content")
	require.NoError(t, err)
}
func TestCAS_FileCopy(t *testing.T) {
	src := env.CasTestNextPath()
	dst := env.CasTestNextPath()
	err := env.cas.FileTestCopy(src, dst, "copy file contents via cas")
	require.NoError(t, err)
}
func TestCAS_Symlink(t *testing.T) {
	src := env.CasTestNextPath()
	dst := env.CasTestNextPath()
	err := env.cas.SymlinkTest(src, dst)
	require.NoError(t, err)
}

func TestCAS_SymlinkStat(t *testing.T) {
	src := env.CasTestNextPath()
	dst := env.CasTestNextPath()
	err := env.cas.SymlinkTestStat(src, dst)
	require.NoError(t, err)
}
func TestCAS_SymlinkNoSourceStat(t *testing.T) {
	src := env.CasTestNextPath()
	dst := env.CasTestNextPath()
	err := env.cas.SymlinkTestNoSourceStat(src, dst)
	require.NoError(t, err)
}
func TestCAS_HardlinkFile(t *testing.T) {
	src := env.CasTestNextPath()
	dst := env.CasTestNextPath()
	err := env.cas.HardLinkFile(src, dst)
	require.NoError(t, err)
}
func TestCAS_DirTestStage(t *testing.T) {
	path := env.CasTestNextPath()
	err := env.cas.DirTestStage(path)
	require.NoError(t, err)
}
func TestCAS_DirTestStageCopy(t *testing.T) {
	srcPath := env.CasTestNextPath()
	cpPath := env.CasTestNextPath()
	err := env.cas.DirTestStageCopy(srcPath, cpPath)
	require.NoError(t, err)
}
func TestCAS_ExcludeDir(t *testing.T) {
	parent := env.CasTestNextPath()
	require.NoError(t, env.cas.ExcludeDirTest(parent, "test-dir"))
}
func TestCAS_ExcludeFile(t *testing.T) {
	parent := env.CasTestNextPath()
	require.NoError(t, env.cas.ExcludeFileTest(parent, "test-file"))
}
func TestCAS_Debug(t *testing.T) {
	dir := env.CasTestNextPath()
	require.NoError(t, env.cas.DebugTests(dir))
}
func TestCAS_Hashes(t *testing.T) {
	require.NoError(t, env.cas.HashTests())
}

func TestCAS_ARISize(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	ari := "test-ari-identifier-12345"
	payload := []byte("hello world payload from ari store")

	require.NoError(t, env.AddARIData(ari, payload))

	path := env.CasTestNextPath()
	err := env.cas.ARISizeTest(path, ari, int64(len(payload)), payload)
	require.NoError(t, err)
}

func TestCAS_ARIWriteAndSnapshot(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	ari := "test-ari-write-snapshot-999"
	origPayload := []byte("original content from ARI store")
	newPayload := []byte("brand new written data payload")

	require.NoError(t, env.AddARIData(ari, origPayload))
	path := env.CasTestNextPath()
	err := env.cas.ARIWriteAndSnapshotTest(path, ari, origPayload, newPayload)
	require.NoError(t, err)
}

func TestCAS_ARISetOnHashedFile(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	path := env.CasTestNextPath()
	ari := "test-ari-hashed-file-check"
	err := env.cas.ARISetOnHashedFileTest(path, "some file content", ari)
	require.NoError(t, err)
}

func TestCAS_ARISetSameARI(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	path := env.CasTestNextPath()
	ari := "test-ari-id-100"
	err := env.cas.ARISetSameARITest(path, ari, 1024)
	require.NoError(t, err)
}

func TestCAS_ARISetDifferentARI(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	path := env.CasTestNextPath()
	ari1 := "test-ari-original"
	ari2 := "test-ari-replacement"
	err := env.cas.ARISetDifferentARITest(path, ari1, ari2, 1024)
	require.NoError(t, err)
}

func TestCAS_ARISetHashOnARIFile(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	path := env.CasTestNextPath()
	ari := "test-ari-no-hash-override"
	err := env.cas.ARISetHashOnARIFileTest(path, ari, 1024)
	require.NoError(t, err)
}

func TestCAS_BackendStatARI(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	ari := "test-ari-backend-stat-101"
	payload := []byte("hello world payload from backend stat ari test")

	require.NoError(t, env.AddARIData(ari, payload))

	path := env.CasTestNextPath()
	err := env.cas.BackendStatARITest(path, ari, int64(len(payload)), payload, 0644)
	require.NoError(t, err)
}

func TestCAS_BackendStatHashedFile(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	path := env.CasTestNextPath()
	content := "sample content block for backend stat hashed file test"

	err := env.cas.BackendStatHashedFileTest(path, content, 0755)
	require.NoError(t, err)
}

func TestCAS_BackendStatDirtyARIFile(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	ari := "test-ari-dirty-backend-stat-202"
	origPayload := []byte("original payload")
	newPayload := []byte("newly modified data payload")

	require.NoError(t, env.AddARIData(ari, origPayload))

	path := env.CasTestNextPath()
	err := env.cas.BackendStatDirtyARIFileTest(path, ari, origPayload, newPayload, 0600)
	require.NoError(t, err)
}
func TestCAS_ARISize_EmbeddedColons(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	// Verify that ARIs containing multiple colons parse size and payload correctly
	ari := "urn:cas:test-ari:12345:colon:embedded:v1"
	payload := []byte("hello world payload from colon-embedded ari store")

	require.NoError(t, env.AddARIData(ari, payload))

	path := env.CasTestNextPath()
	err := env.cas.ARISizeTest(path, ari, int64(len(payload)), payload)
	require.NoError(t, err)
}

func TestCAS_BackendStatARI_EmbeddedColons(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	// Verify BackendStat parsing correctly splits type (ari), payload (with internal colons), size, and mode
	ari := "urn:cas:test-ari:backend-stat:101:embedded:v2"
	payload := []byte("hello world payload from colon-embedded backend stat test")

	require.NoError(t, env.AddARIData(ari, payload))

	path := env.CasTestNextPath()
	err := env.cas.BackendStatARITest(path, ari, int64(len(payload)), payload, 0644)
	require.NoError(t, err)
}

func TestCAS_ARIWriteAndSnapshot_EmbeddedColons(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	ari := "urn:cas:test-ari:write-snapshot:999:extra:colon"
	origPayload := []byte("original content from ARI store")
	newPayload := []byte("brand new written data payload")

	require.NoError(t, env.AddARIData(ari, origPayload))

	path := env.CasTestNextPath()
	err := env.cas.ARIWriteAndSnapshotTest(path, ari, origPayload, newPayload)
	require.NoError(t, err)
}
