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

package git

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
	"github.com/google/abfs/cas/hash/hashers/sha1"
	"github.com/stretchr/testify/require"
)

func hasher() hash.Hasher {
	return sha1.NewHasher()
}
func openStore() (*CASStore, error) {
	tmpDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		return nil, err
	}

	store := NewCASStore(tmpDir, "test", hasher(), true)

	err = store.Open()
	if err != nil {
		return nil, err
	}
	return store, nil
}

// hash for test content used here corresponding to  echo "test_content"
func testContentHash() hash.Hash {
	h, _ := hasher().NewHash("d670460b4b4aece5915caf5c68d12f560a9fe3e4")
	return h
}
func testContentSize() int64 {
	return int64(len("test-content\n"))
}

// hash for symlink used here corresponding to target
func testSymLinkHash() hash.Hash {
	h, _ := hasher().NewHash("1de565933b05f74c75ff9a6520af5f9f8a5a2f1d")
	return h

}
func testSymLinkSize() int64 {
	return int64(len("target"))
}
func destroyStore(s *CASStore) {
	os.RemoveAll(s.RootPath)
}

// echo "test content" | git hash-object -w --stdin
func TestGit_Save(t *testing.T) {
	store, err := openStore()
	require.NoError(t, err)
	defer destroyStore(store)
	data := "test content\n"
	h, err := store.Save(strings.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, testContentHash(), h)

	// Read back from store
	reader, err := store.OpenFile(h)
	require.NoError(t, err)

	c, err := io.ReadAll(reader)
	require.NoError(t, err)

	require.Equal(t, data, string(c))
}

// echo "test content" | git hash-object -w --stdin
func TestGit_SaveFile(t *testing.T) {
	store, err := openStore()
	require.NoError(t, err)
	defer destroyStore(store)
	data := "test content\n"
	f, sz, err := store.Blobs.writeScratchFile(strings.NewReader(data), uncompressed)
	require.NoError(t, err)
	h, err := store.SaveFile(f, sz)
	require.NoError(t, err)
	require.Equal(t, testContentHash(), h)
	reader, err := store.OpenFile(h)
	require.NoError(t, err)

	c, err := io.ReadAll(reader)
	require.NoError(t, err)

	require.Equal(t, data, string(c))
}

// ln -s target sym
func TestGit_SaveSymLink(t *testing.T) {
	store, err := openStore()
	require.NoError(t, err)
	defer destroyStore(store)
	target := "target"

	h, err := store.SaveSymLink(target)
	require.NoError(t, err)
	require.Equal(t, testSymLinkHash(), h)

	b, err := store.GetSymLink(h)
	require.NoError(t, err)
	require.Equal(t, target, b)
}

func dirEntryEqual(t *testing.T, exp, act cas.DirEntry) {
	require.True(t, exp.Hash.Equals(act.Hash))
	require.Equal(t, exp.Size, act.Size)
	require.Equal(t, exp.Mode, act.Mode)
	require.Equal(t, exp.Name, act.Name)
	require.Equal(t, exp.ExtType, act.ExtType)
}

// Tests saving a directory that contains a single empty directory child
func TestGit_SaveDirEntriesEmpty(t *testing.T) {
	store, err := openStore()
	require.NoError(t, err)
	defer destroyStore(store)
	de := cas.DirEntry{Hash: hasher().EmptyDirHash(), Size: 0, Mode: cas.FileModeDir(), Name: "empty"}
	entries := []cas.DirEntry{de}
	h, err := store.SaveDirEntries(cas.NewDirInMemory(entries))
	require.NoError(t, err)
	expHash, err := hasher().NewHash("f4ec99e8174c01eab488469b4c2680500bbb18da")
	require.NoError(t, err)
	require.True(t, expHash.Equals(h))

	reader, err := store.GetDirEntries(h)
	require.NoError(t, err)

	count := 0
	for {
		e, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			break
		}
		dirEntryEqual(t, de, *e)
		count++
	}

	require.NoError(t, err)
	require.Equal(t, 1, count)
}

// Test saving a directory with:
//   - executable
//   - regular file
//   - symlink pointing to regular file
//     echo "test content" > target; echo "test content" >executable; chmod +x executable; ln -s target symlink; git commit -m "test";  git rev-parse main^{tree}
func TestGit_SaveDirEntries(t *testing.T) {
	store, err := openStore()
	require.NoError(t, err)
	defer destroyStore(store)

	// DirEntries
	target := cas.DirEntry{Hash: testContentHash(), Size: testContentSize(), Mode: cas.FileModeFile(), Name: "target"}
	executable := cas.DirEntry{Hash: testContentHash(), Size: testContentSize(), Mode: cas.FileModeExecutable(), Name: "executable"}
	symLink := cas.DirEntry{Hash: testSymLinkHash(), Size: testSymLinkSize(), Mode: cas.FileModeSymLink(), Name: "symlink"}

	// Use out of order to test sorting of NewDirInMemory
	entries := []cas.DirEntry{symLink, executable, target}
	h, err := store.SaveDirEntries(cas.NewDirInMemory(entries))
	require.NoError(t, err)
	expHash, err := hasher().NewHash("291bf4de296e5dc6668bc612ce7e8f72578ccf4c")
	require.NoError(t, err)
	require.True(t, expHash.Equals(h))

	exp := []cas.DirEntry{executable, symLink, target}
	reader, err := store.GetDirEntries(h)
	require.NoError(t, err)

	count := 0
	for {
		e, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			break
		}
		dirEntryEqual(t, exp[count], *e)
		count++
	}

	require.NoError(t, err)
	require.Equal(t, 3, count)

}

// Test saving a directory list
func TestGit_SaveDirList(t *testing.T) {
	store, err := openStore()
	require.NoError(t, err)
	defer destroyStore(store)

	// Use out of order to test sorting
	f1 := cas.DirListEntry{Name: "file-1"}
	f2 := cas.DirListEntry{Name: "file-2"}
	f3 := cas.DirListEntry{Name: "file-3"}
	d1 := cas.DirListEntry{Name: "dir-1"}
	d2 := cas.DirListEntry{Name: "dir-2"}
	d3 := cas.DirListEntry{Name: "dir-3"}

	de := []cas.DirListEntry{f1, f2, f3, d1, d2, d3}

	h, err := store.SaveDirList(cas.NewDirListInMemory(de))
	require.NoError(t, err)
	expHash, err := hasher().NewHash("13ebe1867d7cbaea3551194ac79876a3392727ec")
	require.NoError(t, err)
	require.True(t, expHash.Equals(h))
	// These are not actually stored so no read back
}
