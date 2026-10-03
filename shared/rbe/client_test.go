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
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	"github.com/bazelbuild/remote-apis-sdks/go/pkg/fakes"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/rbe/model"
	"github.com/google/abfs/shared/remote"
)

func TestRBEClient_GetObject_File(t *testing.T) {
	// Arrange
	ctx := context.Background()
	content := "rbe content"
	h := sha256.Sum256([]byte(content))
	sha256Hex := hex.EncodeToString(h[:])
	sha1Hash := entities.NewHash("5fd7706863dc9eeae2d68e20425c5cb64c88330d")

	idStore := &mockIdentityStore{
		mappings: map[entities.Hash]model.Node{
			sha1Hash: {
				Digest: model.Digest{
					SHA256: sha256Hex,
					Size:   int64(len(content)),
				},
				Type: model.NodeTypeFile,
			},
		},
	}

	env, cleanup := fakes.NewTestEnv(t)
	defer cleanup()

	env.Server.CAS.Put([]byte(content))

	c := NewRBEClient(env.Client, idStore)

	// Act
	reader, err := c.GetObject(ctx, sha1Hash)
	require.NoError(t, err)

	got, err := objects.ReadBlob(reader)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, content, string(got))
}

func TestRBEClient_PutObject(t *testing.T) {
	// Arrange
	ctx := context.Background()
	content := "new object content"
	sha1Hash := entities.NewHash("22c39ec3142e6adf6c70d32060004bdf40e8abae")

	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}
	env, cleanup := fakes.NewTestEnv(t)
	defer cleanup()

	c := NewRBEClient(env.Client, idStore)
	reader := remote.NewBlobReaderFromBytes([]byte(content))

	// Act
	err := c.PutObject(ctx, sha1Hash, reader)

	// Assert
	require.NoError(t, err)

	// Verify mapping was created in ID store
	node, found, err := idStore.Get(sha1Hash)
	require.NoError(t, err)
	require.True(t, found)

	h := sha256.Sum256([]byte(content))
	assert.Equal(t, hex.EncodeToString(h[:]), node.Digest.SHA256)
	assert.Equal(t, int64(len(content)), node.Digest.Size)

	// Verify it exists in RBE CAS
	rbeDigest := digest.Digest{Hash: node.Digest.SHA256, Size: node.Digest.Size}
	blob, _, err := env.Client.GrpcClient.ReadBlob(ctx, rbeDigest)
	require.NoError(t, err)
	assert.Equal(t, content, string(blob))
}

func TestRBEClient_Headers(t *testing.T) {
	// Arrange
	ctx := context.Background()
	sha1Hash := entities.NewHash("1a58a9e22709287895e77174e941198642a8b3e8")

	idStore := &mockIdentityStore{
		mappings: map[entities.Hash]model.Node{
			sha1Hash: {
				Digest: model.Digest{
					SHA256: "fake-sha256",
					Size:   1234,
				},
				Type: model.NodeTypeFile,
			},
		},
	}

	env, cleanup := fakes.NewTestEnv(t)
	defer cleanup()

	c := NewRBEClient(env.Client, idStore)

	// Act
	headers, err := c.Headers(ctx, []entities.Hash{sha1Hash})

	// Assert
	require.NoError(t, err)
	require.Len(t, headers, 1)
	assert.Equal(t, int64(1234), headers[0].Size)
	assert.Equal(t, entities.ObjectTypeBlob, headers[0].Type)
}

func TestRBEClient_GetObject_SymlinkSuccess(t *testing.T) {
	// Arrange
	ctx := context.Background()
	target := "target/file.txt"
	h := sha256.Sum256([]byte(target))
	sha256Hex := hex.EncodeToString(h[:])
	sha1Hash := entities.NewHash("0a70f9df502ec612d09618a594b2da8018e69d72")

	idStore := &mockIdentityStore{
		mappings: map[entities.Hash]model.Node{
			sha1Hash: {
				Digest: model.Digest{
					SHA256: sha256Hex,
					Size:   int64(len(target)),
				},
				Type: model.NodeTypeSymlink,
			},
		},
	}

	env, cleanup := fakes.NewTestEnv(t)
	defer cleanup()

	env.Server.CAS.Put([]byte(target))

	c := NewRBEClient(env.Client, idStore)

	// Act
	reader, err := c.GetObject(ctx, sha1Hash)
	require.NoError(t, err)

	got, err := objects.ReadBlob(reader)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, target, string(got))
}

func TestRBEClient_GetObject_Directory(t *testing.T) {
	// Arrange
	ctx := context.Background()
	//
	// 1. Define hashes for the tree
	fileSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	execSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b52")
	symSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b53")
	innerFileSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b54")
	innerSymSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b55")
	gitlinkSha1 := entities.NewHash("f0423896973644771497bdc03eb99d5281615b56")
	innerTreeBytes, _ := gitobjects.EncodeTree([]entities.FileEntry{
		{Name: "inner_file.txt", Mode: entities.FileModeRegular, Hash: innerFileSha1},
		{Name: "inner_link", Mode: entities.FileModeSymlink, Hash: innerSymSha1},
	})
	innerDirSha1 := entities.Hash(sha1.Sum(innerTreeBytes))
	rootTreeBytes, _ := gitobjects.EncodeTree([]entities.FileEntry{
		{Name: "file.txt", Mode: entities.FileModeRegular, Hash: fileSha1},
		{Name: "gitlink_node", Mode: entities.FileModeSubmodule, Hash: gitlinkSha1},
		{Name: "script.sh", Mode: entities.FileModeExecutable, Hash: execSha1},
		{Name: "subdir", Mode: entities.FileModeDir, Hash: innerDirSha1},
		{Name: "link_to_file", Mode: entities.FileModeSymlink, Hash: symSha1},
	})
	rootDirSha1 := entities.Hash(sha1.Sum(rootTreeBytes))

	// 2. Create the inner directory proto (subdir/)
	innerDir := &repb.Directory{
		Files: []*repb.FileNode{
			{
				Name:   "inner_file.txt",
				Digest: &repb.Digest{Hash: "inner-sha256", SizeBytes: 10},
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: innerFileSha1.String()}},
				},
			},
		},
		Symlinks: []*repb.SymlinkNode{
			{
				Name:   "inner_link",
				Target: "inner_file.txt",
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: innerSymSha1.String()}},
				},
			},
		},
	}
	innerDir.NodeProperties = &repb.NodeProperties{
		Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: innerDirSha1.String()}},
	}
	innerData, _ := proto.Marshal(innerDir)
	innerH := sha256.Sum256(innerData)
	innerSHA256 := hex.EncodeToString(innerH[:])

	// 2b. Create a gitlink directory proto (gitlink_node/)
	gitlinkDir := &repb.Directory{
		NodeProperties: &repb.NodeProperties{
			Properties: []*repb.NodeProperty{{Name: "gitlink", Value: gitlinkSha1.String()}},
		},
	}
	gitlinkData, _ := proto.Marshal(gitlinkDir)
	gitlinkH := sha256.Sum256(gitlinkData)
	gitlinkSHA256 := hex.EncodeToString(gitlinkH[:])

	// 3. Create the root directory proto (/)
	rootDir := &repb.Directory{
		Files: []*repb.FileNode{
			{
				Name:   "file.txt",
				Digest: &repb.Digest{Hash: "file-sha256", SizeBytes: 20},
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: fileSha1.String()}},
				},
			},
			{
				Name:   "script.sh",
				Digest: &repb.Digest{Hash: "exec-sha256", SizeBytes: 30},
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: execSha1.String()}},
				},
				IsExecutable: true,
			},
		},
		Directories: []*repb.DirectoryNode{
			{
				Name:   "subdir",
				Digest: &repb.Digest{Hash: innerSHA256, SizeBytes: int64(len(innerData))},
			},
			{
				Name:   "gitlink_node",
				Digest: &repb.Digest{Hash: gitlinkSHA256, SizeBytes: int64(len(gitlinkData))},
			},
		},
		Symlinks: []*repb.SymlinkNode{
			{
				Name:   "link_to_file",
				Target: "file.txt",
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: symSha1.String()}},
				},
			},
		},
	}
	rootDir.NodeProperties = &repb.NodeProperties{
		Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: rootDirSha1.String()}},
	}
	rootData, _ := proto.Marshal(rootDir)
	rootH := sha256.Sum256(rootData)
	rootSHA256 := hex.EncodeToString(rootH[:])

	// 4. Setup RBE CAS and identity mappings
	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}
	idStore.mappings[rootDirSha1] = model.Node{
		Digest: model.Digest{SHA256: rootSHA256, Size: int64(len(rootData))},
		Type:   model.NodeTypeDirectory,
	}

	// 5. Setup RBE CAS and identity mappings
	env, cleanup := fakes.NewTestEnv(t)
	defer cleanup()
	env.Server.CAS.Put(rootData)
	env.Server.CAS.Put(innerData)
	env.Server.CAS.Put(gitlinkData)
	c := NewRBEClient(env.Client, idStore)

	// Act
	reader, err := c.GetObject(ctx, rootDirSha1)
	require.NoError(t, err)

	// Assert
	assert.NotNil(t, reader)

	// Verify the Bundled Results
	bundled, ok := reader.(remote.BundledReadCloser)
	require.True(t, ok)

	// BLOBS: Should contain targets for "link_to_file" and "inner_link"
	blobs := bundled.Blobs()
	assert.Len(t, blobs, 2)
	blobMap := make(map[entities.Hash]string)
	for _, b := range blobs {
		blobMap[b.Hash] = string(b.Data)
	}
	assert.Equal(t, "file.txt", blobMap[symSha1])
	assert.Equal(t, "inner_file.txt", blobMap[innerSymSha1])

	// Objects should contain the translated tree for all reachable directories except gitlinks,
	// and the compiled deeptree.
	gotObjects := bundled.Objects()
	assert.Len(t, gotObjects, 2)

	var dirObj, deeptreeObj *remote.BundledObject
	for i := range gotObjects {
		if gotObjects[i].Hash == innerDirSha1 {
			dirObj = &gotObjects[i]
		} else {
			deeptreeObj = &gotObjects[i]
		}
	}
	require.NotNil(t, dirObj, "should find inner directory object")
	require.NotNil(t, deeptreeObj, "should find compiled deeptree object")

	// Verify the inner directory content
	innerEntries, err := entities.DecodeDirPlain(dirObj.Data)
	require.NoError(t, err)
	assert.Len(t, innerEntries, 2)
	innerEntryMap := make(map[string]entities.FileEntry)
	for _, e := range innerEntries {
		innerEntryMap[e.Name] = e
	}
	assert.Equal(t, innerFileSha1, innerEntryMap["inner_file.txt"].Hash)
	assert.Equal(t, innerSymSha1, innerEntryMap["inner_link"].Hash)

	// Verify mapping was recorded in BundledMaps
	req := &objects.DeepTreeRequest{
		Version: objects.CurrentDeepTreeRequestVersion,
		Tree:    rootDirSha1,
	}
	require.NoError(t, req.Encode())
	bundledMaps := bundled.MapResults()
	require.Len(t, bundledMaps, 1)
	assert.Equal(t, req.Hash, bundledMaps[0].From)
	assert.Equal(t, deeptreeObj.Hash, bundledMaps[0].To)

	// Verify the compiled DeepTree object's content
	parsedDeepTree := new(gitobjects.DeepTreeObject)
	err = parsedDeepTree.Decode(deeptreeObj.Hash, deeptreeObj.Data)
	require.NoError(t, err)

	require.Len(t, parsedDeepTree.Entries, 8)

	// Root
	assert.Equal(t, "", parsedDeepTree.Entries[0].Name)
	assert.Equal(t, entities.FileModeDir, parsedDeepTree.Entries[0].Mode)
	assert.Equal(t, int64(8), parsedDeepTree.Entries[0].Size)
	assert.Equal(t, rootDirSha1, parsedDeepTree.Entries[0].Hash)

	// file.txt
	assert.Equal(t, "file.txt", parsedDeepTree.Entries[1].Name)
	assert.Equal(t, entities.FileModeRegular, parsedDeepTree.Entries[1].Mode)
	assert.Equal(t, int64(20), parsedDeepTree.Entries[1].Size)
	assert.Equal(t, fileSha1, parsedDeepTree.Entries[1].Hash)

	// script.sh
	assert.Equal(t, "script.sh", parsedDeepTree.Entries[2].Name)
	assert.Equal(t, entities.FileModeExecutable, parsedDeepTree.Entries[2].Mode)
	assert.Equal(t, int64(30), parsedDeepTree.Entries[2].Size)
	assert.Equal(t, execSha1, parsedDeepTree.Entries[2].Hash)

	// link_to_file
	assert.Equal(t, "link_to_file", parsedDeepTree.Entries[3].Name)
	assert.Equal(t, entities.FileModeSymlink, parsedDeepTree.Entries[3].Mode)
	assert.Equal(t, int64(8), parsedDeepTree.Entries[3].Size)
	assert.Equal(t, symSha1, parsedDeepTree.Entries[3].Hash)

	// subdir
	assert.Equal(t, "subdir", parsedDeepTree.Entries[4].Name)
	assert.Equal(t, entities.FileModeDir, parsedDeepTree.Entries[4].Mode)
	assert.Equal(t, int64(3), parsedDeepTree.Entries[4].Size)
	assert.Equal(t, innerDirSha1, parsedDeepTree.Entries[4].Hash)

	// inner_file.txt
	assert.Equal(t, "inner_file.txt", parsedDeepTree.Entries[5].Name)
	assert.Equal(t, entities.FileModeRegular, parsedDeepTree.Entries[5].Mode)
	assert.Equal(t, int64(10), parsedDeepTree.Entries[5].Size)
	assert.Equal(t, innerFileSha1, parsedDeepTree.Entries[5].Hash)

	// inner_link
	assert.Equal(t, "inner_link", parsedDeepTree.Entries[6].Name)
	assert.Equal(t, entities.FileModeSymlink, parsedDeepTree.Entries[6].Mode)
	assert.Equal(t, int64(14), parsedDeepTree.Entries[6].Size)
	assert.Equal(t, innerSymSha1, parsedDeepTree.Entries[6].Hash)

	// gitlink_node
	assert.Equal(t, "gitlink_node", parsedDeepTree.Entries[7].Name)
	assert.Equal(t, entities.FileModeSubmodule, parsedDeepTree.Entries[7].Mode)
	assert.Equal(t, int64(0), parsedDeepTree.Entries[7].Size)
	assert.Equal(t, gitlinkSha1, parsedDeepTree.Entries[7].Hash)

	// 7. Verify Root Content
	treeBytes, err := io.ReadAll(reader)
	require.NoError(t, err)

	// Verify it's a valid ABFS tree
	entries, err := entities.DecodeDirPlain(treeBytes)
	require.NoError(t, err)

	assert.Len(t, entries, 5)
	entryMap := make(map[string]entities.FileEntry)
	for _, e := range entries {
		entryMap[e.Name] = e
	}

	assert.Equal(t, fileSha1, entryMap["file.txt"].Hash)
	assert.Equal(t, entities.FileModeRegular, entryMap["file.txt"].Mode)

	assert.Equal(t, execSha1, entryMap["script.sh"].Hash)
	assert.Equal(t, entities.FileModeExecutable, entryMap["script.sh"].Mode)

	assert.Equal(t, symSha1, entryMap["link_to_file"].Hash)
	assert.Equal(t, entities.FileModeSymlink, entryMap["link_to_file"].Mode)

	assert.Equal(t, innerDirSha1, entryMap["subdir"].Hash)
	assert.Equal(t, entities.FileModeDir, entryMap["subdir"].Mode)

	assert.Equal(t, gitlinkSha1, entryMap["gitlink_node"].Hash)
	assert.Equal(t, entities.FileModeSubmodule, entryMap["gitlink_node"].Mode)

	// Verify identity store was primed for all elements in the tree (excluding gitlinks)
	for _, h := range []entities.Hash{innerDirSha1, fileSha1, execSha1, symSha1, innerFileSha1, innerSymSha1} {
		_, found, _ := idStore.Get(h)
		assert.True(t, found, "Identity store should be primed for hash %s", h)
	}
	_, found, _ := idStore.Get(gitlinkSha1)
	assert.False(t, found, "Identity store should NOT be primed for gitlink %s", gitlinkSha1)
	assert.Equal(t, rootTreeBytes, treeBytes)

}

func TestRBEClient_GetObject_Directory_MissingGitSHA1Fallback(t *testing.T) {
	// Arrange
	ctx := context.Background()

	// 1. Define hashes and data for files
	fileSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")

	// 2. Create the inner directory proto (subdir/) without any git_sha1 property!
	innerDir := &repb.Directory{
		Files: []*repb.FileNode{
			{
				Name:   "inner_file.txt",
				Digest: &repb.Digest{Hash: "inner-sha256", SizeBytes: 10},
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: "a0423896973644771497bdc03eb99d5281615b54"}},
				},
			},
		},
	}
	// Omit: innerDir.NodeProperties (no git_sha1!)
	innerData, _ := proto.Marshal(innerDir)
	innerH := sha256.Sum256(innerData)
	innerSHA256 := hex.EncodeToString(innerH[:])

	// Calculate the expected fallback hash for the subdirectory:
	// TranslateDirectory and addSubdirectories calculate it as: fallbackDirHash(innerSHA256)
	innerDirFallbackSha1 := fallbackDirHash(innerSHA256)

	// 3. Create the root directory proto (/) without any git_sha1 property!
	rootDir := &repb.Directory{
		Files: []*repb.FileNode{
			{
				Name:   "file.txt",
				Digest: &repb.Digest{Hash: "file-sha256", SizeBytes: 20},
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: fileSha1.String()}},
				},
			},
		},
		Directories: []*repb.DirectoryNode{
			{
				Name:   "subdir",
				Digest: &repb.Digest{Hash: innerSHA256, SizeBytes: int64(len(innerData))},
			},
		},
	}
	// Omit: rootDir.NodeProperties (no git_sha1!)
	rootData, _ := proto.Marshal(rootDir)
	rootH := sha256.Sum256(rootData)
	rootSHA256 := hex.EncodeToString(rootH[:])

	// The root Git SHA-1 passed to GetObject. Since rootDir has no git_sha1,
	// the client must fall back to using this passed key.
	expectedRootDirSha1 := entities.NewHash("f000000000000000000000000000000000000001")

	// 4. Setup mock identity store.
	// We map the rootDirSha1 (which is the expectedRootDirSha1 fallback) to the root RBE directory digest.
	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}
	idStore.mappings[expectedRootDirSha1] = model.Node{
		Digest: model.Digest{SHA256: rootSHA256, Size: int64(len(rootData))},
		Type:   model.NodeTypeDirectory,
	}

	// 5. Setup RBE server CAS
	env, cleanup := fakes.NewTestEnv(t)
	defer cleanup()
	env.Server.CAS.Put(rootData)
	env.Server.CAS.Put(innerData)
	c := NewRBEClient(env.Client, idStore)

	// Act
	reader, err := c.GetObject(ctx, expectedRootDirSha1)
	require.NoError(t, err)
	assert.NotNil(t, reader)

	// Assert: Verify the Bundled Results
	bundled, ok := reader.(remote.BundledReadCloser)
	require.True(t, ok)

	// Objects should contain:
	// 1. The translated tree for the inner directory (bundled under innerDirFallbackSha1)
	// 2. The compiled deeptree.
	gotObjects := bundled.Objects()
	assert.Len(t, gotObjects, 2)

	var dirObj, deeptreeObj *remote.BundledObject
	for i := range gotObjects {
		if gotObjects[i].Hash == innerDirFallbackSha1 {
			dirObj = &gotObjects[i]
		} else {
			deeptreeObj = &gotObjects[i]
		}
	}
	require.NotNil(t, dirObj, "should find inner directory object translated and bundled under the expected fallback hash")
	require.NotNil(t, deeptreeObj, "should find compiled deeptree object")

	// Verify the compiled DeepTree object's content
	parsedDeepTree := new(gitobjects.DeepTreeObject)
	err = parsedDeepTree.Decode(deeptreeObj.Hash, deeptreeObj.Data)
	require.NoError(t, err)

	require.Len(t, parsedDeepTree.Entries, 4)

	// Root entry should have the fallback rootGitHash (expectedRootDirSha1)
	assert.Equal(t, "", parsedDeepTree.Entries[0].Name)
	assert.Equal(t, entities.FileModeDir, parsedDeepTree.Entries[0].Mode)
	assert.Equal(t, expectedRootDirSha1, parsedDeepTree.Entries[0].Hash)

	// file.txt entry
	assert.Equal(t, "file.txt", parsedDeepTree.Entries[1].Name)
	assert.Equal(t, fileSha1, parsedDeepTree.Entries[1].Hash)

	// Subdir entry should have the fallback innerDirFallbackSha1
	assert.Equal(t, "subdir", parsedDeepTree.Entries[2].Name)
	assert.Equal(t, entities.FileModeDir, parsedDeepTree.Entries[2].Mode)
	assert.Equal(t, innerDirFallbackSha1, parsedDeepTree.Entries[2].Hash)

	// inner_file.txt entry
	assert.Equal(t, "inner_file.txt", parsedDeepTree.Entries[3].Name)
	assert.Equal(t, entities.FileModeRegular, parsedDeepTree.Entries[3].Mode)
	assert.Equal(t, entities.NewHash("a0423896973644771497bdc03eb99d5281615b54"), parsedDeepTree.Entries[3].Hash)

	// Verify identity store was primed for the fallback subdirectory hash
	_, found, _ := idStore.Get(innerDirFallbackSha1)
	assert.True(t, found, "Identity store should be primed for the fallback subdirectory hash %s", innerDirFallbackSha1)
}

func TestRBEClient_GetObject_Directory_DoubleMapping(t *testing.T) {
	// Arrange
	ctx := context.Background()

	actualRootTreeSha1 := entities.NewHash("a000000000000000000000000000000000000001")
	requestedKey := entities.NewHash("f000000000000000000000000000000000000002")

	// 1. Create a root directory with an explicit git_sha1 property
	rootDir := &repb.Directory{
		NodeProperties: &repb.NodeProperties{
			Properties: []*repb.NodeProperty{
				{Name: "git_sha1", Value: actualRootTreeSha1.String()},
			},
		},
	}
	rootData, _ := proto.Marshal(rootDir)
	rootH := sha256.Sum256(rootData)
	rootSHA256 := hex.EncodeToString(rootH[:])

	// 2. Setup mock identity store mapping the requested key to the root directory digest
	idStore := &mockIdentityStore{mappings: make(map[entities.Hash]model.Node)}
	idStore.mappings[requestedKey] = model.Node{
		Digest: model.Digest{SHA256: rootSHA256, Size: int64(len(rootData))},
		Type:   model.NodeTypeDirectory,
	}

	// 3. Setup RBE server CAS
	env, cleanup := fakes.NewTestEnv(t)
	defer cleanup()
	env.Server.CAS.Put(rootData)
	c := NewRBEClient(env.Client, idStore)

	// Act
	reader, err := c.GetObject(ctx, requestedKey)
	require.NoError(t, err)
	assert.NotNil(t, reader)

	// Assert
	bundled, ok := reader.(remote.BundledReadCloser)
	require.True(t, ok)

	gotObjects := bundled.Objects()
	// Should contain the compiled deeptree.
	require.Len(t, gotObjects, 1)
	deeptreeObj := gotObjects[0]

	// Calculate expected DeepTreeRequest hashes
	req1 := &objects.DeepTreeRequest{
		Version: objects.CurrentDeepTreeRequestVersion,
		Tree:    requestedKey,
	}
	require.NoError(t, req1.Encode())

	req2 := &objects.DeepTreeRequest{
		Version: objects.CurrentDeepTreeRequestVersion,
		Tree:    actualRootTreeSha1,
	}
	require.NoError(t, req2.Encode())

	// Verify that both DeepTreeRequests map to the same DeepTree object
	maps := bundled.MapResults()
	require.Len(t, maps, 2)

	expectedFroms := map[entities.Hash]bool{
		req1.Hash: true,
		req2.Hash: true,
	}

	for _, m := range maps {
		assert.True(t, expectedFroms[m.From], "Unexpected From hash: %s", m.From)
		assert.Equal(t, deeptreeObj.Hash, m.To)
	}
}

type duplicateTrackingIdentityStore struct {
	mockIdentityStore
	putCounts map[entities.Hash]int
}

func (d *duplicateTrackingIdentityStore) PutMany(entries []IdentityEntry) error {
	if d.putCounts == nil {
		d.putCounts = make(map[entities.Hash]int)
	}
	for _, e := range entries {
		d.putCounts[e.SHA1]++
	}
	return d.mockIdentityStore.PutMany(entries)
}

func TestRBEClient_Compile_NilIdentityStore(t *testing.T) {
	// Arrange
	fileSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	rootDir := &repb.Directory{
		Files: []*repb.FileNode{
			{
				Name:   "file.txt",
				Digest: &repb.Digest{Hash: "file-sha256", SizeBytes: 20},
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: fileSha1.String()}},
				},
			},
		},
	}
	protoMap, gitlinkMap, sha1Map, err := indexRBEDirectories([]*repb.Directory{rootDir})
	require.NoError(t, err)

	var rootDigest digest.Digest
	for d := range protoMap {
		rootDigest = d
	}

	compiler := &deeptreeCompiler{
		client:     &RBEClient{identityStore: nil},
		protoMap:   protoMap,
		gitlinkMap: gitlinkMap,
		sha1Map:    sha1Map,
		rootDigest: rootDigest,
	}

	// Act
	err = compiler.compile()

	// Assert
	require.NoError(t, err)
	assert.Empty(t, compiler.identityEntries)
}

func TestRBEClient_Compile_NoDuplicateDirectoryEntries(t *testing.T) {
	// Arrange: Create nested directory tree (root -> subdir -> inner_file.txt)
	innerFileSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b54")
	innerDirSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b55")
	rootDirSha1 := entities.NewHash("a0423896973644771497bdc03eb99d5281615b50")

	innerDir := &repb.Directory{
		Files: []*repb.FileNode{
			{
				Name:   "inner_file.txt",
				Digest: &repb.Digest{Hash: "inner-sha256", SizeBytes: 10},
				NodeProperties: &repb.NodeProperties{
					Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: innerFileSha1.String()}},
				},
			},
		},
		NodeProperties: &repb.NodeProperties{
			Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: innerDirSha1.String()}},
		},
	}
	innerData, _ := proto.Marshal(innerDir)
	innerH := sha256.Sum256(innerData)
	innerSHA256 := hex.EncodeToString(innerH[:])

	rootDir := &repb.Directory{
		Directories: []*repb.DirectoryNode{
			{
				Name:   "subdir",
				Digest: &repb.Digest{Hash: innerSHA256, SizeBytes: int64(len(innerData))},
			},
		},
		NodeProperties: &repb.NodeProperties{
			Properties: []*repb.NodeProperty{{Name: "git_sha1", Value: rootDirSha1.String()}},
		},
	}

	protoMap, gitlinkMap, sha1Map, err := indexRBEDirectories([]*repb.Directory{rootDir, innerDir})
	require.NoError(t, err)

	var rootDigest digest.Digest
	for d, proto := range protoMap {
		if proto == rootDir {
			rootDigest = d
		}
	}

	trackingStore := &duplicateTrackingIdentityStore{
		mockIdentityStore: mockIdentityStore{mappings: make(map[entities.Hash]model.Node)},
		putCounts:         make(map[entities.Hash]int),
	}

	compiler := &deeptreeCompiler{
		client:     &RBEClient{identityStore: trackingStore},
		protoMap:   protoMap,
		gitlinkMap: gitlinkMap,
		sha1Map:    sha1Map,
		rootDigest: rootDigest,
	}

	// Act
	err = compiler.compile()

	// Assert
	require.NoError(t, err)
	// Verify each entry (rootDir, innerDir, innerFile) was written exactly once
	assert.Equal(t, 1, trackingStore.putCounts[rootDirSha1], "Root directory should be written exactly once")
	assert.Equal(t, 1, trackingStore.putCounts[innerDirSha1], "Subdirectory should be written exactly once")
	assert.Equal(t, 1, trackingStore.putCounts[innerFileSha1], "File should be written exactly once")
}
