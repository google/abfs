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
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/fs"
	casfstests "github.com/google/abfs/cas/fs/tests"
	"github.com/stretchr/testify/require"
)

func apiSetup(t *testing.T) (string, fs.FSAPI) {
	api := env.createAPI()
	path := ""
	return path, api
}

// Mkdir
func TestAPI_Mkdir(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIMkdir(api, path.Join(root, "dir"), cas.FileModeDir()))
}
func TestAPI_MkdirFail(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIMkdirFail(api, path.Join(root, "dir"), cas.FileModeDir()))
}
func TestAPI_MkdirDepth(t *testing.T) {
	root, api := apiSetup(t)
	_, err := casfstests.APIMkdirDepth(api, root, 256)
	require.NoError(t, err)
}
func TestAPI_MkdirAll(t *testing.T) {
	root, api := apiSetup(t)
	for _ = range 125 {
		root = filepath.Join(root, "dir")
	}
	require.NoError(t, casfstests.APIMkdirAll(api, root, cas.FileModeDir()))
}

// Remove
func TestAPI_Rmdir(t *testing.T) {
	root, api := apiSetup(t)
	dir := path.Join(root, "dir")
	require.NoError(t, casfstests.APIMkdir(api, dir, cas.FileModeDir()))
	require.NoError(t, casfstests.APIRemove(api, dir))
}
func TestAPI_RmdirNotEmpty(t *testing.T) {
	root, api := apiSetup(t)
	dir := path.Join(root, "dir")
	require.NoError(t, casfstests.APIRemoveDirNotEmpty(api, dir))
}
func TestAPI_Remove(t *testing.T) {
	root, api := apiSetup(t)
	fpath := path.Join(root, "nod")
	require.NoError(t, casfstests.APIMknod(api, fpath, cas.FileModeFile(), 0))
	require.NoError(t, casfstests.APIRemove(api, fpath))
}
func TestAPI_RemoveDoesNotExist(t *testing.T) {
	root, api := apiSetup(t)
	fpath := path.Join(root, "nod")
	require.NoError(t, casfstests.APIRemoveDoesNotExist(api, fpath))
}
func TestAPI_DirRemove(t *testing.T) {
	root, api := apiSetup(t)
	dir, err := casfstests.APIMkdirDepth(api, root, 256)
	require.NoError(t, err)
	require.NoError(t, casfstests.APIRemove(api, dir))
}

// Rename
func TestAPI_Rename(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIMknod(api, path.Join(root, "nod"), cas.FileModeFile(), 0))
	dir, err := casfstests.APIMkdirDepth(api, root, 256)
	require.NoError(t, err)
	require.NoError(t, casfstests.APIRename(api, path.Join(root, "nod"), path.Join(dir, "newnod")))
}
func TestAPI_RenameExists(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIMknod(api, path.Join(root, "nod"), cas.FileModeFile(), 0))
	dir, err := casfstests.APIMkdirDepth(api, root, 256)
	require.NoError(t, err)
	require.NoError(t, casfstests.APIMknod(api, path.Join(root, "new"), cas.FileModeFile(), 0))
	require.NoError(t, casfstests.APIRename(api, path.Join(root, "nod"), path.Join(dir, "newnod")))
}
func TestAPI_RenameDir(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIMkdir(api, path.Join(root, "tgt"), cas.FileModeDir()))
	dir, err := casfstests.APIMkdirDepth(api, root, 256)
	require.NoError(t, err)
	require.NoError(t, casfstests.APIRename(api, path.Join(root, "tgt"), path.Join(dir, "newdir")))
}
func TestAPI_RenameInvalidSource(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIRenameInvalidPath(api, path.Join(root, "tgt"), path.Join(root, "tgt-2")))
}
func TestAPI_RenameInvalidDest(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIRenameInvalidPath(api, root, path.Join(root, "subdir", "tgt-2")))
}

// Symlink
func TestAPI_Symlink(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APISymlinkToDir(api, path.Join(root, "dir"), path.Join(root, "symlink")))
}
func TestAPI_SymlinkExists(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APISymlinkExists(api, path.Join(root, "dir"), path.Join(root, "symlink")))
}
func TestAPI_SymlinkToDepthDir(t *testing.T) {
	root, api := apiSetup(t)
	dir, err := casfstests.APIMkdirDepth(api, root, 256)
	require.NoError(t, err)
	require.NoError(t, casfstests.APISymlinkToDir(api, path.Join(dir, "target"), filepath.Join(root, "symlink")))
}

// Readlink
func TestAPI_ReadLink(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIReadLink(api, path.Join(root, "sub_dir", "target"), path.Join(root, "sym")))
}
func TestAPI_ReadLinkInvalid(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIReadLinkInvalid(api, path.Join(root, "path")))
}

// Hardlink
func TestAPI_Link(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIMknod(api, path.Join(root, "nod"), cas.FileModeFile(), 0))
	dir, err := casfstests.APIMkdirDepth(api, root, 256)
	require.NoError(t, err)
	require.NoError(t, casfstests.APILink(api, path.Join(root, "nod"), filepath.Join(dir, "hlink")))
}
func TestAPI_LinkSymlink(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIMknod(api, path.Join(root, "nod"), cas.FileModeFile(), 0))
	require.NoError(t, casfstests.APISymlink(api, path.Join(root, "nod"), path.Join(root, "symlink")))
	require.NoError(t, casfstests.APILinkSymlink(api, path.Join(root, "symlink"), path.Join(root, "hlink")))
}

// Mknod
func TestAPI_MknodFile(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIMknod(api, path.Join(root, "nod"), cas.FileModeFile(), 0))
}
func TestAPI_MknodSocket(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIMknod(api, path.Join(root, "nod"), cas.FileModeSocket(), 0))
}
func TestAPI_MknodFifo(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APIMknod(api, path.Join(root, "nod"), cas.FileModeFifo(), 0))
}

// OpenFile
func TestAPI_OpenCreateFile(t *testing.T) {
	root, api := apiSetup(t)
	require.NoError(t, casfstests.APICreateTest(api, path.Join(root, "file")))
}

// OpenFile (create+readonly)
func TestAPI_OpenCreateReadOnly(t *testing.T) {
	root, api := apiSetup(t)
	_, err := casfstests.APIOpenFile(api, path.Join(root, "file"), os.O_RDONLY|os.O_CREATE, cas.FileModeFile())
	require.NoError(t, err)
}

// OpenFile (write only | no create -- fail)
func TestAPI_OpenNoCreate(t *testing.T) {
	root, api := apiSetup(t)
	_, err := casfstests.APIOpenFile(api, path.Join(root, "file"), os.O_WRONLY, cas.FileModeFile())
	require.Error(t, err)
}

func TestAPI_RenameSubDir(t *testing.T) {
	root, api := apiSetup(t)
	newpath := root
	dir, err := casfstests.APIMkdirDepth(api, root, 256)
	require.NoError(t, err)
	_, err = api.Stat(dir)
	require.NoError(t, err)

	for _ = range 127 {
		newpath = filepath.Join(newpath, "dir")
	}
	oldpath := filepath.Join(newpath, "dir")
	newpath = filepath.Join(newpath, "TARGET")

	require.NoError(t, casfstests.APIRename(api, oldpath, newpath))
	// Old path should no longer work
	_, err = api.Stat(dir)
	require.Error(t, err)

	for _ = range 128 {
		newpath = filepath.Join(newpath, "dir")
	}

	_, err = api.Stat(newpath)
	require.NoError(t, err)
}
