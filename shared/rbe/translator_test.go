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

package rbe

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/rbe/model"
)

func TestTranslateDirectory(t *testing.T) {
	// Arrange
	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}

	fileSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	symSha1 := entities.NewHash("b0423896973644771497bdc03eb99d5281615b52")

	dir := &repb.Directory{
		Files: []*repb.FileNode{
			{
				Name: "file.txt",
				Digest: &repb.Digest{
					Hash:      "file-sha256",
					SizeBytes: 100,
				},
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{
						{Name: "git_sha1", Value: fileSha1.String()},
					},
				},
			},
		},
		Symlinks: []*repb.SymlinkNode{
			{
				Name:   "link",
				Target: "file.txt",
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{
						{Name: "git_sha1", Value: symSha1.String()},
					},
				},
			},
		},
	}

	// Act
	m := make(map[digest.Digest]entities.Hash)
	treeBytes, bundled, err := TranslateDirectory(dir, idStore, digest.Digest{}, nil, m)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, treeBytes)
	require.Len(t, bundled, 1)
	assert.Equal(t, []byte("file.txt"), bundled[0].Data)
	assert.Equal(t, symSha1, bundled[0].Hash)

	// Verify identity store priming
	dFile, found, _ := idStore.Get(fileSha1)
	assert.True(t, found)
	assert.Equal(t, "file-sha256", dFile.Digest.SHA256)
	assert.Equal(t, int64(100), dFile.Digest.Size)
	assert.Equal(t, model.NodeTypeFile, dFile.Type)

	dSym, found, _ := idStore.Get(symSha1)
	assert.True(t, found)
	assert.Equal(t, model.NodeTypeSymlink, dSym.Type)
	expectedHash := sha256.Sum256([]byte("file.txt"))
	assert.Equal(t, hex.EncodeToString(expectedHash[:]), dSym.Digest.SHA256)
	assert.Equal(t, int64(len("file.txt")), dSym.Digest.Size)

	// Verify encoded tree content
	entries, err := entities.DecodeDirPlain(treeBytes)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Sorted by name: file.txt, link
	assert.Equal(t, "file.txt", entries[0].Name)
	assert.Equal(t, entities.FileModeRegular, entries[0].Mode)
	assert.Equal(t, fileSha1, entries[0].Hash)

	assert.Equal(t, "link", entries[1].Name)
	assert.Equal(t, entities.FileModeSymlink, entries[1].Mode)
	assert.Equal(t, symSha1, entries[1].Hash)
}

func TestTranslateDirectory_GitSHA1(t *testing.T) {
	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}

	symSha1 := entities.NewHash("9960d71e470abffea069afc686dfb7a17525d382")

	dir := &repb.Directory{
		Symlinks: []*repb.SymlinkNode{
			{
				Name:   "symlink_to_file_sh",
				Target: "./file.sh",
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{
						{Name: "git_sha1", Value: symSha1.String()},
					},
				},
			},
		},
	}

	// Act
	m := make(map[digest.Digest]entities.Hash)
	treeBytes, bundled, err := TranslateDirectory(dir, idStore, digest.Digest{}, nil, m)
	require.NoError(t, err)
	require.NotNil(t, treeBytes)
	require.Len(t, bundled, 1)
	assert.Equal(t, []byte("./file.sh"), bundled[0].Data)
	assert.Equal(t, symSha1, bundled[0].Hash)

	dSym, found, _ := idStore.Get(symSha1)
	assert.True(t, found)
	assert.Equal(t, model.NodeTypeSymlink, dSym.Type)
	assert.Equal(t, int64(len("./file.sh")), dSym.Digest.Size)

	entries, err := entities.DecodeDirPlain(treeBytes)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "symlink_to_file_sh", entries[0].Name)
	assert.Equal(t, symSha1, entries[0].Hash)
}

func TestTranslateDirectory_MissingSHA1(t *testing.T) {
	// Arrange
	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}

	dir := &repb.Directory{
		Files: []*repb.FileNode{
			{
				Name: "file-no-sha1",
				Digest: &repb.Digest{
					Hash:      "sha256-abc",
					SizeBytes: 123,
				},
			},
		},
		Directories: []*repb.DirectoryNode{
			{
				Name: "dir-no-sha1",
				Digest: &repb.Digest{
					Hash:      "sha256-def",
					SizeBytes: 0,
				},
			},
		},
	}

	// Act
	m := make(map[digest.Digest]entities.Hash)
	treeBytes, bundled, err := TranslateDirectory(dir, idStore, digest.Digest{}, nil, m)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, treeBytes)
	assert.Empty(t, bundled)

	// Verify encoded tree content
	entries, err := entities.DecodeDirPlain(treeBytes)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Verify first entry (dir-no-sha1) - sorted by name
	assert.Equal(t, "dir-no-sha1", entries[0].Name)
	assert.Equal(t, entities.FileModeDir, entries[0].Mode)

	// Verify second entry (file-no-sha1)
	assert.Equal(t, "file-no-sha1", entries[1].Name)
	assert.Equal(t, entities.FileModeRegular, entries[1].Mode)

	// Verify identity store priming for both
	dDir, found, _ := idStore.Get(entries[0].Hash)
	assert.True(t, found)
	assert.Equal(t, "sha256-def", dDir.Digest.SHA256)
	assert.Equal(t, model.NodeTypeDirectory, dDir.Type)

	dFile, found, _ := idStore.Get(entries[1].Hash)
	assert.True(t, found)
	assert.Equal(t, "sha256-abc", dFile.Digest.SHA256)
	assert.Equal(t, model.NodeTypeFile, dFile.Type)
}

func TestTranslateDirectory_SymlinkOnlyGitSHA1(t *testing.T) {
	// Arrange
	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}
	symSha1 := entities.NewHash("9960d71e470abffea069afc686dfb7a17525d382")

	dir := &repb.Directory{
		Symlinks: []*repb.SymlinkNode{
			{
				Name:   "symlink_to_file.txt",
				Target: "./file.txt",
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{
						{Name: "git_sha1", Value: symSha1.String()},
					},
				},
			},
		},
	}

	// Act
	m := make(map[digest.Digest]entities.Hash)
	treeBytes, bundled, err := TranslateDirectory(dir, idStore, digest.Digest{}, nil, m)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, treeBytes)
	require.Len(t, bundled, 1)
	assert.Equal(t, []byte("./file.txt"), bundled[0].Data)
	assert.Equal(t, symSha1, bundled[0].Hash)

	dSym, found, _ := idStore.Get(symSha1)
	assert.True(t, found)
	assert.Equal(t, model.NodeTypeSymlink, dSym.Type)
	expectedHash := sha256.Sum256([]byte("./file.txt"))
	assert.Equal(t, hex.EncodeToString(expectedHash[:]), dSym.Digest.SHA256)
	assert.Equal(t, int64(len("./file.txt")), dSym.Digest.Size)
	entries, err := entities.DecodeDirPlain(treeBytes)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "symlink_to_file.txt", entries[0].Name)
	assert.Equal(t, symSha1, entries[0].Hash)
}

func TestTranslateDirectory_Symlink(t *testing.T) {
	// Arrange
	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}

	symSha1 := entities.NewHash("c0423896973644771497bdc03eb99d5281615b53")
	symRbeHash := "0598f63785e861873948ea039f8d069adf93a4e8c6ada20a7925967677a9bddd"
	symRbeSize := int64(12)

	dir := &repb.Directory{
		Symlinks: []*repb.SymlinkNode{
			{
				Name:   "link",
				Target: "file.txt",
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{
						{Name: "git_sha1", Value: symSha1.String()},
						{Name: "digest", Value: fmt.Sprintf("%s/%d", symRbeHash, symRbeSize)},
					},
				},
			},
		},
	}

	// Act
	m := make(map[digest.Digest]entities.Hash)
	_, _, err := TranslateDirectory(dir, idStore, digest.Digest{}, nil, m)

	// Assert
	require.NoError(t, err)

	dSym, found, _ := idStore.Get(symSha1)
	assert.True(t, found)
	assert.Equal(t, symRbeHash, dSym.Digest.SHA256)
	assert.Equal(t, symRbeSize, dSym.Digest.Size)
}

func TestTranslateDirectory_Gitlink(t *testing.T) {
	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}

	glName := "external-repo"
	glSha1 := entities.NewHash("f0423896973644771497bdc03eb99d5281615b56")
	glRbeSha256 := "gitlink-rbe-sha256"

	dir := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			{
				Name: glName,
				Digest: &repb.Digest{
					Hash:      glRbeSha256,
					SizeBytes: 0, // MUST BE 0 for gitlink
				},
			},
		},
	}

	gitlinkMap := map[digest.Digest]entities.Hash{
		{Hash: glRbeSha256, Size: 0}: glSha1,
	}

	treeBytes, _, err := TranslateDirectory(
		dir, idStore, digest.Digest{}, gitlinkMap, make(map[digest.Digest]entities.Hash))
	require.NoError(t, err)

	// Case 1: Gitlinks should NOT be added to identity store priming
	_, found, _ := idStore.Get(glSha1)
	assert.False(t, found)

	// Verify encoded tree content
	entries, err := entities.DecodeDirPlain(treeBytes)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	assert.Equal(t, glName, entries[0].Name)
	assert.Equal(t, entities.FileModeSubmodule, entries[0].Mode)
	assert.Equal(t, glSha1, entries[0].Hash)
}

func TestTranslateDirectory_RegularWithSHA1(t *testing.T) {
	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}

	dirName := "sub-dir"
	dirSha1 := entities.NewHash("e0423896973644771497bdc03eb99d5281615b55")
	dirRbeSha256 := "dir-rbe-sha256"

	dir := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			{
				Name: dirName,
				Digest: &repb.Digest{
					Hash:      dirRbeSha256,
					SizeBytes: 100, // Non-empty
				},
			},
		},
	}

	sha1Map := map[digest.Digest]entities.Hash{
		{Hash: dirRbeSha256, Size: 100}: dirSha1,
	}

	treeBytes, _, err := TranslateDirectory(
		dir, idStore, digest.Digest{}, make(map[digest.Digest]entities.Hash), sha1Map)
	require.NoError(t, err)

	// Case 2: Regular directories with SHA-1 SHOULD be added to identity store priming
	dDir, found, _ := idStore.Get(dirSha1)
	assert.True(t, found)
	assert.Equal(t, dirRbeSha256, dDir.Digest.SHA256)
	assert.Equal(t, model.NodeTypeDirectory, dDir.Type)

	// Verify encoded tree content
	entries, err := entities.DecodeDirPlain(treeBytes)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	assert.Equal(t, dirName, entries[0].Name)
	assert.Equal(t, entities.FileModeDir, entries[0].Mode)
	assert.Equal(t, dirSha1, entries[0].Hash)
}

func TestTranslateDirectoryEntries_Preallocation(t *testing.T) {
	fileSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	symSha1 := entities.NewHash("b0423896973644771497bdc03eb99d5281615b52")
	dirSha1 := entities.NewHash("c0423896973644771497bdc03eb99d5281615b53")

	dir := &repb.Directory{
		Files: []*repb.FileNode{
			{
				Name:   "file.txt",
				Digest: &repb.Digest{Hash: "file-sha256", SizeBytes: 100},
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: fileSha1.String()}},
				},
			},
		},
		Directories: []*repb.DirectoryNode{
			{
				Name:   "subdir",
				Digest: &repb.Digest{Hash: "dir-sha256", SizeBytes: 50},
			},
		},
		Symlinks: []*repb.SymlinkNode{
			{
				Name:   "link",
				Target: "file.txt",
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: symSha1.String()}},
				},
			},
		},
	}

	sha1Map := map[digest.Digest]entities.Hash{
		{Hash: "dir-sha256", Size: 50}: dirSha1,
	}

	treeBytes, bundled, idEntries, err := TranslateDirectoryEntries(dir, digest.Digest{}, nil, sha1Map)
	require.NoError(t, err)
	assert.NotNil(t, treeBytes)
	assert.Len(t, bundled, 1)
	assert.Len(t, idEntries, 3)
	assert.Equal(t, 3, cap(idEntries))
}
