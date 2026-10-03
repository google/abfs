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

func TestTap_FileEncodingBackslash(t *testing.T) {
	file := "str\\\\escape.rscript"
	err := env.tap.FileEncodingTest(env.rootDir, env.TapTestNextPath(), file)
	require.NoError(t, err)
}
func TestTap_FileEncodingBackslashSingle(t *testing.T) {
	file := "str\\escape.rscript"
	err := env.tap.FileEncodingTest(env.rootDir, env.TapTestNextPath(), file)
	require.NoError(t, err)

}
func TestTap_FileEncodingQuote(t *testing.T) {
	file := "str\"escape.rscript"
	err := env.tap.FileEncodingTest(env.rootDir, env.TapTestNextPath(), file)
	require.NoError(t, err)
}

func TestTap_FileRename(t *testing.T) {
	err := env.tap.RenameTest(env.rootDir, env.TapTestNextPath(), "string.old", "string.new", "string.new")
	require.NoError(t, err)
}
func TestTap_FileEncodingRename(t *testing.T) {
	// rename is run through sh -c so need to account for encoding change in
	// expected name
	err := env.tap.RenameTest(env.rootDir, env.TapTestNextPath(), "string.old", "string.\\\\new", "string.\\new")
	require.NoError(t, err)
}

func TestTap_WriteRemove(t *testing.T) {
	require.NoError(t, env.tap.WriteRemoveTest(env.rootDir, env.TapTestNextPath()))
}

func TestTap_HardLinkTest(t *testing.T) {
	require.NoError(t, env.tap.HardLinkTest(env.rootDir, env.TapTestNextPath()))
}
func TestTap_Mkdir(t *testing.T) {
	require.NoError(t, env.tap.MkdirTest(env.rootDir, env.TapTestNextPath()))
}
func TestTap_Rmdir(t *testing.T) {
	require.NoError(t, env.tap.RmdirTest(env.rootDir, env.TapTestNextPath()))
}
func TestTap_SymlinkDir(t *testing.T) {
	require.NoError(t, env.tap.SymlinkDirTest(env.rootDir, env.TapTestNextPath()))
}
func TestTap_SymlinkFile(t *testing.T) {
	require.NoError(t, env.tap.SymlinkFileTest(env.rootDir, env.TapTestNextPath()))
}
func TestTap_ReadOnlyCreate(t *testing.T) {
	require.NoError(t, env.tap.ReadOnlyCreateTest(env.rootDir, env.TapTestNextPath()))
}
func TestTap_ReadOnlyExecCreate(t *testing.T) {
	require.NoError(t, env.tap.ReadOnlyCreateExecTest(env.rootDir, env.TapTestNextPath()))
}
func TestTap_ChmodTest(t *testing.T) {
	require.NoError(t, env.tap.ChmodTest(env.rootDir, env.TapTestNextPath()))
}

func TestTapResolved_FileRead(t *testing.T) {
	require.NoError(t, env.tap.FileReadResolved(env.rootDir, env.TapTestNextPath()))
}
func TestTapResolved_FileCreateDeleteMkdir(t *testing.T) {
	require.NoError(t, env.tap.FileCreateDeleteMkdirResolved(env.rootDir, env.TapTestNextPath()))
}
func TestTapResolved_FileCreateRenameResolved(t *testing.T) {
	require.NoError(t, env.tap.FileCreateRenameResolved(env.rootDir, env.TapTestNextPath()))
}
func TestTapResolved_FileRenameDirResolved(t *testing.T) {
	require.NoError(t, env.tap.FileRenameDirResolved(env.rootDir, env.TapTestNextPath()))
}
func TestTap_ARITapRead(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	ari := "test-ari-tap-identifier-999"
	payload := []byte("hello world tapped ari content payload")
	require.NoError(t, env.AddARIData(ari, payload))
	err := env.tap.ARITapReadTest(env.rootDir, env.TapTestNextPath(), ari, payload)
	require.NoError(t, err)
}
func TestTapResolved_ARIWrite(t *testing.T) {
	if !env.SupportsARI() {
		t.Skip("Skipping ARI test in FUSE mode")
	}
	ari := "test-ari-tap-resolved-write-888"
	origPayload := []byte("original ari content")
	newPayload := []byte("newly modified tap content payload")
	require.NoError(t, env.AddARIData(ari, origPayload))
	err := env.tap.ARITapWriteResolved(env.rootDir, env.TapTestNextPath(), ari, origPayload, newPayload)
	require.NoError(t, err)
}
