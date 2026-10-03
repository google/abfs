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
	iofs "io/fs"
	"testing"

	"github.com/google/abfs/cas/cas"
	"github.com/stretchr/testify/require"
)

func TestFS_Mkdir(t *testing.T) {
	d := env.createRootDir()
	require.NoError(t, FsMkdirTest(t.Context(), d, "test"))
}
func TestFS_MkdirExist(t *testing.T) {
	d := env.createRootDir()
	require.NoError(t, FsMkdirTest(t.Context(), d, "test"))
	require.Error(t, FsMkdirTest(t.Context(), d, "test"))
}
func TestFS_MkdirExistOverwrite(t *testing.T) {
	d := env.createRootDir()
	require.NoError(t, FsMkdirTest(t.Context(), d, "test"))
	mode := cas.FileModeFile()
	require.Error(t, FsCreateFile(t.Context(), d, "test", mode))

}
func TestFS_Rmdir(t *testing.T) {
	d := env.createRootDir()
	require.NoError(t, FsRmdirTest(t.Context(), d, "test"))
}
func TestFS_RmdirNonExisting(t *testing.T) {
	d := env.createRootDir()
	require.NoError(t, FsRmdirNonExists(t.Context(), d))
}
func TestFS_TmpFile(t *testing.T) {
	d := env.createRootDir()
	mode := cas.FileModeFile()
	// Don't support this currently at fs layer
	require.Error(t, FsCreateTmpFile(t.Context(), d, mode))
}
func TestFS_CreateFile(t *testing.T) {
	d := env.createRootDir()
	mode := cas.FileModeFile()
	require.NoError(t, FsCreateFile(t.Context(), d, "file", mode))
}
func TestFS_CreateFileExists(t *testing.T) {
	d := env.createRootDir()
	mode := cas.FileModeFile()
	require.NoError(t, FsCreateFile(t.Context(), d, "file", mode))
	require.NoError(t, FsCreateFile(t.Context(), d, "file", mode))
}

func TestFS_CreateExecutable(t *testing.T) {
	d := env.createRootDir()
	mode := cas.FileModeExecutable()
	require.NoError(t, FsCreateFile(t.Context(), d, "file", mode))
}
func TestFS_Symlink(t *testing.T) {
	d := env.createRootDir()
	require.NoError(t, FsCreateSymlink(t.Context(), d, "symlink", "file"))
}
func TestFS_SymlinkSet(t *testing.T) {
	d := env.createRootDir()
	require.NoError(t, FsSetSymlink(t.Context(), d, "symlink", "file", "file2"))
}
func TestFS_SimpleRename(t *testing.T) {
	d := env.createRootDir()
	oldName := "oldfile"
	newName := "newfile"
	mode := cas.FileModeFile()
	require.NoError(t, FsCreateFile(t.Context(), d, oldName, mode))
	require.NoError(t, FsRename(t.Context(), d, oldName, d, newName, mode))
}
func TestFS_Rename(t *testing.T) {
	d := env.createRootDir()
	require.NoError(t, FsRenameTest(t.Context(), d))
}

func TestFS_MknodFile(t *testing.T) {
	d := env.createRootDir()
	mode := cas.FileModeFile()
	require.NoError(t, FsMknod(t.Context(), d, "file", mode, 0))
}
func TestFS_MknodFileExists(t *testing.T) {
	d := env.createRootDir()
	mode := cas.FileModeFile()
	require.NoError(t, FsMknod(t.Context(), d, "file", mode, 0))
	require.Error(t, FsMknod(t.Context(), d, "file", mode, 0))
}
func TestFS_MknodExecutable(t *testing.T) {
	d := env.createRootDir()
	mode := cas.FileModeExecutable()
	require.NoError(t, FsMknod(t.Context(), d, "exec", mode, 0))
}
func TestFS_MknodSocket(t *testing.T) {
	d := env.createRootDir()
	mode := cas.FileModeSocket()
	require.NoError(t, FsMknod(t.Context(), d, "socket", mode, 0))

}
func TestFS_MknodFifo(t *testing.T) {
	d := env.createRootDir()
	mode := cas.FileModeFifo()
	require.NoError(t, FsMknod(t.Context(), d, "fifo", mode, 0))

}
func TestFS_MknodUnsupported(t *testing.T) {
	d := env.createRootDir()
	var mode iofs.FileMode = iofs.ModeDevice
	require.Error(t, FsMknod(t.Context(), d, "dev", mode, 0))
}
func TestFS_SimpleLink(t *testing.T) {
	d := env.createRootDir()
	require.NoError(t, FsSimpleLink(t.Context(), d, "name1", "name2"))

}
func TestFS_FileOps(t *testing.T) {
	d := env.createRootDir()
	require.NoError(t, FsFileOps(t.Context(), d, "file"))
}
