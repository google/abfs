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

package cas

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// use the getReadStore() as a remote end point and provider test to populate
// the local store

func createRemoteReader(t *testing.T) (*readStore, *RemoteReader) {
	rs, err := getReadStore()
	local := newTestStore()
	require.NoError(t, err)
	p := &test_provider{remote: rs.store, local: local}
	r := NewRemoteReader(local, p)
	return rs, r
}

func TestRemoteReader_OpenFile(t *testing.T) {
	rs, r := createRemoteReader(t)
	for _, h := range rs.fileHashes {
		c, err := r.OpenFile(h)
		require.NoError(t, err)
		require.NoError(t, c.Close())
	}

}
func TestRemoteReader_GetSymLink(t *testing.T) {
	rs, r := createRemoteReader(t)
	for _, h := range rs.symLinkHashes {
		_, err := r.GetSymLink(h)
		require.NoError(t, err)
	}
}
func TestRemoteReader_GetDirList(t *testing.T) {
	rs, r := createRemoteReader(t)
	for _, h := range rs.dirListHashes {
		c, err := r.GetDirList(h)
		require.NoError(t, err)
		require.NoError(t, c.Close())
	}
}
func TestRemoteReader_GetDirEntries(t *testing.T) {
	rs, r := createRemoteReader(t)
	for _, h := range rs.dirEntryHashes {
		c, err := r.GetDirEntries(h)
		require.NoError(t, err)
		require.NoError(t, c.Close())
	}
}
func TestRemoteReader_GetDeepTree(t *testing.T) {
	rs, r := createRemoteReader(t)
	for _, h := range rs.deepTreeHashes {
		c, err := r.GetDeepTree(h)
		require.NoError(t, err)
		require.NoError(t, c.Close())
	}
}

// Writer tests push data through to the remote

func createRemoteWriter(t *testing.T) (*RemoteWriter, Store) {
	remote := newTestStore()
	local := newTestStore()
	p := &test_provider{remote: remote, local: local}
	w := NewRemoteWriter(local, p)
	return w, remote
}

func TestRemoteWriter_SaveFile(t *testing.T) {
	w, remoteStore := createRemoteWriter(t)
	h, err := w.SaveFile("/path/to/a/file", 99)
	require.NoError(t, err)
	c, err := remoteStore.OpenFile(h)
	require.NoError(t, err)
	require.NoError(t, c.Close())
}

func TestRemoteWriter_SaveSymLink(t *testing.T) {
	w, remoteStore := createRemoteWriter(t)
	h, err := w.SaveSymLink("/path/target")
	require.NoError(t, err)
	_, err = remoteStore.GetSymLink(h)
	require.NoError(t, err)
}

func TestRemoteWriter_SaveDirList(t *testing.T) {
	sz := 100
	list := make([]DirListEntry, sz)
	for i := range sz {
		list[i] = DirListEntry{Name: fmt.Sprintf("entry-%d", i)}
	}
	dl := NewDirListInMemory(list)

	w, remoteStore := createRemoteWriter(t)
	h, err := w.SaveDirList(dl)
	require.NoError(t, err)
	c, err := remoteStore.GetDirList(h)
	require.NoError(t, err)
	require.NoError(t, c.Close())
}

func TestRemoteWriter_SaveDirEntries(t *testing.T) {
	sz := 200
	list := make([]DirEntry, sz)
	for i := range sz {
		fname := fmt.Sprintf("dir-entry-%d", i)
		list[i] = DirEntry{
			Hash: hashFile(fname, 77),
			Size: 77,
			Mode: FileModeFile(),
			Name: fname,
		}
	}
	dl := NewDirInMemory(list)

	w, remoteStore := createRemoteWriter(t)
	h, err := w.SaveDirEntries(dl)
	require.NoError(t, err)
	c, err := remoteStore.GetDirEntries(h)
	require.NoError(t, err)
	require.NoError(t, c.Close())
}

func TestRemoteWriter_SaveDeepTree(t *testing.T) {
	sz := 200
	list := make([]DirEntry, sz)
	for i := range sz {
		fname := fmt.Sprintf("dir-entry-%d", i)
		list[i] = DirEntry{
			Hash: hashFile(fname, 77),
			Size: 77,
			Mode: FileModeFile(),
			Name: fname,
		}
	}
	dl := NewDirInMemory(list)

	w, remoteStore := createRemoteWriter(t)
	h, err := w.SaveDeepTree(dl)
	require.NoError(t, err)
	c, err := remoteStore.GetDeepTree(h)
	require.NoError(t, err)
	require.NoError(t, c.Close())

}
