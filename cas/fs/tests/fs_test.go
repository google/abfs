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

// Create a directory and check that stat matches
func TestFS_Dir(t *testing.T) {
	path := env.FsTestNextPath()
	err := MkdirTest(path)
	require.NoError(t, err)
}
func TestFS_Rmdir(t *testing.T) {
	path := env.FsTestNextPath()
	err := RmdirTest(path)
	require.NoError(t, err)
}
func TestFS_File(t *testing.T) {
	path := env.FsTestNextPath()
	err := FileTest(path)
	require.NoError(t, err)
}
func TestFS_FileContent(t *testing.T) {
	path := env.FsTestNextPath()
	err := FileTestContent(path, "Test file data\n")
	require.NoError(t, err)
}
func TestFS_FileTruncate(t *testing.T) {
	path := env.FsTestNextPath()
	err := FileTestTruncate(path, "Test file data\n")
	require.NoError(t, err)
}
func TestFS_FileTruncateOpen(t *testing.T) {
	path := env.FsTestNextPath()
	err := FileTestTruncateOpen(path, "Test file data\n")
	require.NoError(t, err)
}
func TestFS_FileAppend(t *testing.T) {
	path := env.FsTestNextPath()
	err := FileTestContentAppend(path, "Test file data\n", "...append")
	require.NoError(t, err)

}
func TestFS_NonFile(t *testing.T) {
	path := env.FsTestNextPath()
	err := FileNotExistsTest(path)
	require.NoError(t, err)

}

func TestFS_MknodFile(t *testing.T) {
	path := env.FsTestNextPath()
	err := MknodFileTest(path)
	require.NoError(t, err)
}
func TestFS_MknodFifo(t *testing.T) {
	path := env.FsTestNextPath()
	err := MknodFifoTest(path)
	require.NoError(t, err)
}
func TestFS_MknodSocket(t *testing.T) {
	path := env.FsTestNextPath()

	err := MknodSocketTest(path)
	require.NoError(t, err)
}
func TestFS_SymlinkFile(t *testing.T) {
	targetPath := env.FsTestNextPath()
	symPath := env.FsTestNextPath()

	err := SymlinkTest(targetPath, symPath)
	require.NoError(t, err)
}
func TestFS_SymlinkFileNoSource(t *testing.T) {
	targetPath := env.FsTestNextPath()
	symPath := env.FsTestNextPath()

	err := SymlinkTestNoSource(targetPath, symPath)
	require.NoError(t, err)
}
func TestFS_HardlinkFile(t *testing.T) {
	targetPath := env.FsTestNextPath()
	linkPath := env.FsTestNextPath()
	err := HardLinkFile(targetPath, linkPath)
	require.NoError(t, err)
}
func TestFS_HardlinkFileDiffDir(t *testing.T) {
	d1 := env.FsTestNextPath()
	f1 := "target"
	linkPath := "link"
	d2 := env.FsTestNextPath()
	err := HardLinkFileDiffDir(d1, f1, d2, linkPath)
	require.NoError(t, err)
}
func TestFS_HardlinkSymlink(t *testing.T) {
	targetPath := env.FsTestNextPath()
	symlinkPath := env.FsTestNextPath()
	linkPath := env.FsTestNextPath()
	err := HardLinkSymlink(targetPath, symlinkPath, linkPath)
	require.NoError(t, err)
}
func TestFS_HardlinkDir(t *testing.T) {
	targetPath := env.FsTestNextPath()
	linkPath := env.FsTestNextPath()
	err := HardLinkDir(targetPath, linkPath)
	require.NoError(t, err)
}
func TestFS_HardLinkFileAfterDelete(t *testing.T) {
	targetPath := env.FsTestNextPath()
	linkPath := env.FsTestNextPath()
	err := HardLinkFileAfterDelete(targetPath, linkPath)
	require.NoError(t, err)
}
func TestFS_HardLinkSymlinkAfterDelete(t *testing.T) {
	targetPath := env.FsTestNextPath()
	symlinkPath := env.FsTestNextPath()
	linkPath := env.FsTestNextPath()
	err := HardLinkSymlinkAfterDelete(targetPath, symlinkPath, linkPath)
	require.NoError(t, err)
}
func TestFS_DirTestStage(t *testing.T) {
	path := env.FsTestNextPath()
	err := DirTestStage(path)
	require.NoError(t, err)
}

func TestFS_FileToExec(t *testing.T) {
	require.NoError(t, FileExecTest(env.FsTestNextPath()))
}
func TestFS_FileReadOnlyWrite(t *testing.T) {
	require.NoError(t, FileReadOnlyWrite(env.FsTestNextPath()))
}
