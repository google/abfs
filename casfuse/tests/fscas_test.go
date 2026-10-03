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

func TestFSCAS_Mkdir(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsMkdirTest(t.Context(), d, "test"))
}
func TestFSCAS_MkdirExist(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsMkdirTest(t.Context(), d, "test"))
	require.Error(t, FsMkdirTest(t.Context(), d, "test"))
}
func TestFSCAS_MkdirExistOverwrite(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsMkdirTest(t.Context(), d, "test"))
	require.Error(t, FsCreateFile(t.Context(), d, "test", cas.FileModeFile()))

}
func TestFSCAS_Rmdir(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsRmdirTest(t.Context(), d, "test"))
}
func TestFSCAS_RmdirNonExisting(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsRmdirNonExists(t.Context(), d))
}
func TestFSCAS_TmpFile(t *testing.T) {
	d, _ := env.createRootCASDir()
	// Don't support this currently at fs layer
	require.Error(t, FsCreateTmpFile(t.Context(), d, cas.FileModeFile()))
}
func TestFSCAS_CreateFile(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsCreateFile(t.Context(), d, "file", cas.FileModeFile()))
}
func TestFSCAS_CreateFileExists(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsCreateFile(t.Context(), d, "file", cas.FileModeFile()))
	require.NoError(t, FsCreateFile(t.Context(), d, "file", cas.FileModeFile()))
}

func TestFSCAS_CreateExecutable(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsCreateFile(t.Context(), d, "file", cas.FileModeExecutable()))
}
func TestFSCAS_Symlink(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsCreateSymlink(t.Context(), d, "symlink", "file"))
}
func TestFSCAS_SymlinkSet(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsSetSymlink(t.Context(), d, "symlink", "file", "file2"))
}
func TestFSCAS_SimpleRename(t *testing.T) {
	d, _ := env.createRootCASDir()
	oldName := "oldfile"
	newName := "newfile"
	require.NoError(t, FsCreateFile(t.Context(), d, oldName, cas.FileModeFile()))
	require.NoError(t, FsRename(t.Context(), d, oldName, d, newName, cas.FileModeFile()))
}
func TestFSCAS_Rename(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsRenameTest(t.Context(), d))
}

func TestFSCAS_MknodFile(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsMknod(t.Context(), d, "file", cas.FileModeFile(), 0))
}
func TestFSCAS_MknodFileExists(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsMknod(t.Context(), d, "file", cas.FileModeFile(), 0))
	require.Error(t, FsMknod(t.Context(), d, "file", cas.FileModeFile(), 0))
}
func TestFSCAS_MknodExecutable(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsMknod(t.Context(), d, "exec", cas.FileModeExecutable(), 0))
}
func TestFSCAS_MknodSocket(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsMknod(t.Context(), d, "socket", cas.FileModeSocket(), 0))

}
func TestFSCAS_MknodFifo(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsMknod(t.Context(), d, "fifo", cas.FileModeFifo(), 0))

}
func TestFSCAS_MknodUnsupported(t *testing.T) {
	d, _ := env.createRootCASDir()
	var mode iofs.FileMode = iofs.ModeDevice
	require.Error(t, FsMknod(t.Context(), d, "dev", mode, 0))
}
func TestFSCAS_SimpleLink(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsSimpleLink(t.Context(), d, "name1", "name2"))

}
func TestFSCAS_FileOps(t *testing.T) {
	d, _ := env.createRootCASDir()
	require.NoError(t, FsFileOps(t.Context(), d, "file"))
}
