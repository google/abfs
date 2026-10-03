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

package filesystem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/manifest"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	rbemodel "github.com/google/abfs/shared/rbe/model"
)

func TestProjectGitConfig(t *testing.T) {
	in := `<manifest>
 <remote name="goog" fetch=".." review="https://android-review.googlesource.com"></remote>
 <default remote="goog"/>
 <project name="platform/build/blueprint" path="build/blueprint1"></project>
 <project name="platform/build/soong" path="build/soong"></project>
 <project name="platform/build/blueprint" path="build/blueprint2"></project>
 <remove-project name="platform/build/blueprint"></remove-project>
 <project name="platform/build/blueprint" path="build/blueprint3"></project>
 <project name="platform/art" path="art"></project>
</manifest>`

	m, err := manifest.Parse([]byte(in))
	require.NoError(t, err)

	tests := []struct {
		name string
		info ProjectInfo
	}{
		{
			name: "Normal Project",
			info: ProjectInfo{
				Manifest:         m,
				Project:          m.Project[0],
				SchemeManProjURL: "https://android.googlesource.com/platform/manifest",
			},
		},
		{
			// "abfs git-clone"
			name: "No manifest",
			info: ProjectInfo{},
		},
	}
	blueprintRemote := `[remote "goog"]
	fetch = +refs/heads/*:refs/remotes/goog/*
	url = https://android.googlesource.com/platform/build/blueprint
	review = https://android-review.googlesource.com
	projectname = platform/build/blueprint
`
	want := []string{
		configTemplate + blueprintRemote + casfsTemplate,
		configTemplate + casfsTemplate,
	}
	for idx, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, err := ProjectGitConfig(test.info)
			require.NoError(t, err)
			require.Equal(t, want[idx], string(s))

			cfg := config.New()
			require.NoError(t, config.NewDecoder(bytes.NewReader(s)).Decode(cfg))
		})
	}
}

func TestGetRBEDigest(t *testing.T) {
	tempDir := t.TempDir()
	mockFs := &mockCasfs{mountPoint: tempDir}
	m := &CasfsMount{
		mountPoint: tempDir,
		casfs:      mockFs,
	}

	// 1. Regular file
	content := []byte("hello world\n")
	filePath := filepath.Join(tempDir, "file.txt")
	require.NoError(t, os.WriteFile(filePath, content, 0644))
	require.NoError(t, os.Chmod(filePath, 0644))

	h := sha256.Sum256(content)
	expectedSha := hex.EncodeToString(h[:])
	expectedSize := int64(len(content))

	resp, err := m.GetRBEDigest(context.Background(), "file.txt")
	require.NoError(t, err)
	require.Equal(t, "file.txt", resp.Path)
	require.Equal(t, expectedSha, resp.SHA256)
	require.Equal(t, expectedSize, resp.Size)
	require.Equal(t, uint32(syscall.S_IFREG|0644), resp.Mode)

	// 2. Executable file
	execPath := filepath.Join(tempDir, "script.sh")
	require.NoError(t, os.WriteFile(execPath, content, 0755))
	require.NoError(t, os.Chmod(execPath, 0755))

	respExec, err := m.GetRBEDigest(context.Background(), "script.sh")
	require.NoError(t, err)
	require.Equal(t, "script.sh", respExec.Path)
	require.Equal(t, expectedSha, respExec.SHA256)
	require.Equal(t, expectedSize, respExec.Size)
	require.Equal(t, uint32(syscall.S_IFREG|0755), respExec.Mode)

	// 3. Symlink
	symlinkTarget := "target/sub/file.txt"
	symlinkPath := filepath.Join(tempDir, "symlink.txt")
	require.NoError(t, os.Symlink(symlinkTarget, symlinkPath))

	symH := sha256.Sum256([]byte(symlinkTarget))
	expectedSymSha := hex.EncodeToString(symH[:])
	expectedSymSize := int64(len(symlinkTarget))

	respSym, err := m.GetRBEDigest(context.Background(), "symlink.txt")
	require.NoError(t, err)
	require.Equal(t, "symlink.txt", respSym.Path)
	require.Equal(t, expectedSymSha, respSym.SHA256)
	require.Equal(t, expectedSymSize, respSym.Size)
	require.Equal(t, uint32(syscall.S_IFLNK), respSym.Mode&syscall.S_IFMT)

	// 4. Dangling symlink (target does not exist)
	danglingTarget := "nonexistent_target.txt"
	danglingPath := filepath.Join(tempDir, "dangling.txt")
	require.NoError(t, os.Symlink(danglingTarget, danglingPath))

	dangH := sha256.Sum256([]byte(danglingTarget))
	expectedDangSha := hex.EncodeToString(dangH[:])
	expectedDangSize := int64(len(danglingTarget))

	respDang, err := m.GetRBEDigest(context.Background(), "dangling.txt")
	require.NoError(t, err)
	require.Equal(t, "dangling.txt", respDang.Path)
	require.Equal(t, expectedDangSha, respDang.SHA256)
	require.Equal(t, expectedDangSize, respDang.Size)
	require.Equal(t, uint32(syscall.S_IFLNK), respDang.Mode&syscall.S_IFMT)

	// 5. Directory (should fail)
	subDir := filepath.Join(tempDir, "subdir")
	require.NoError(t, os.Mkdir(subDir, 0755))

	_, err = m.GetRBEDigest(context.Background(), "subdir")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot get digest of a directory")
}

func TestSetRBEDigest_RejectsDirectory(t *testing.T) {
	m := &CasfsMount{}
	err := m.SetRBEDigest(context.Background(), "some/dir", rbemodel.Digest{SHA256: "abc", Size: 10}, syscall.S_IFDIR|0755)
	require.Error(t, err)
	require.Contains(t, err.Error(), "only file blobs are supported")
}

func TestSetRBEDigest_RejectsNotFullyQualified(t *testing.T) {
	m := &CasfsMount{}
	err := m.SetRBEDigest(context.Background(), "file.txt", rbemodel.Digest{SHA256: "abc", Size: 10}, 0644)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not fully qualified")
}

type mockCasfs struct {
	bindcas.CASFS
	mountPoint string
	mknodModes map[string]entities.FileMode
	dirtyMap   map[string]bool
	hashes     map[string]entities.Hash
	sizes      map[string]int64
}

func (m *mockCasfs) MkdirAll(name string, mode entities.FileMode) error {
	osMode, _ := mode.ToOSFileMode()
	return os.MkdirAll(filepath.Join(m.mountPoint, name), osMode)
}

func (m *mockCasfs) Lstat(path string) (cas.FileInfo, error) {
	fi, err := os.Lstat(filepath.Join(m.mountPoint, path))
	if err != nil {
		return nil, err
	}
	return fi, nil
}

func (m *mockCasfs) Mknod(p string, mode entities.FileMode, rDev int) error {
	if m.mknodModes == nil {
		m.mknodModes = make(map[string]entities.FileMode)
	}
	m.mknodModes[p] = mode
	osMode, _ := mode.ToOSFileMode()
	f, err := os.OpenFile(filepath.Join(m.mountPoint, p), os.O_CREATE|os.O_EXCL|os.O_WRONLY, osMode)
	if err != nil {
		return err
	}
	return f.Close()
}

func (m *mockCasfs) Chmod(p string, mode cas.FileMode) error {
	return os.Chmod(filepath.Join(m.mountPoint, p), mode)
}

func (m *mockCasfs) SetARISize(p, ari string, s int64) error {
	return nil
}

func (m *mockCasfs) ARISize(path string) (string, int64, error) {
	return "", 0, nil
}

func (m *mockCasfs) Dirty(path string) (bool, error) {
	if m.dirtyMap != nil {
		return m.dirtyMap[path], nil
	}
	return false, nil
}

func (m *mockCasfs) HashSize(path string) (entities.Hash, int64, error) {
	if m.hashes != nil {
		return m.hashes[path], m.sizes[path], nil
	}
	return entities.ZeroHash, 0, nil
}

func TestSetRBEDigest_FullyQualifiedModes(t *testing.T) {
	tempDir := t.TempDir()
	mockFs := &mockCasfs{mountPoint: tempDir}
	m := &CasfsMount{casfs: mockFs, mountPoint: tempDir}

	// 1. Fully qualified 0100644 (S_IFREG | 0644)
	err := m.SetRBEDigest(context.Background(), "file644.txt", rbemodel.Digest{SHA256: "abc", Size: 10}, syscall.S_IFREG|0644)
	require.NoError(t, err)
	require.Equal(t, entities.FileMode(syscall.S_IFREG|0644), mockFs.mknodModes["file644.txt"])

	// 2. Fully qualified 0100600 (S_IFREG | 0600)
	err = m.SetRBEDigest(context.Background(), "file600.txt", rbemodel.Digest{SHA256: "abc", Size: 10}, syscall.S_IFREG|0600)
	require.NoError(t, err)
	require.Equal(t, entities.FileMode(syscall.S_IFREG|0600), mockFs.mknodModes["file600.txt"])

	// 3. Fully qualified 0100755 (S_IFREG | 0755)
	err = m.SetRBEDigest(context.Background(), "file755.txt", rbemodel.Digest{SHA256: "abc", Size: 10}, syscall.S_IFREG|0755)
	require.NoError(t, err)
	require.Equal(t, entities.FileMode(syscall.S_IFREG|0755), mockFs.mknodModes["file755.txt"])
}

func TestRBEPathStat_JSON(t *testing.T) {
	stat := RBEPathStat{
		Path: "foo/bar.txt",
		Digest: rbemodel.Digest{
			SHA256: "abcdef123456",
			Size:   1024,
		},
		Mode: 0100644,
	}

	data, err := json.Marshal(stat)
	require.NoError(t, err)
	require.JSONEq(t, `{"path":"foo/bar.txt","sha256":"abcdef123456","size":1024,"mode":33188}`, string(data))

	var decoded RBEPathStat
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, stat, decoded)
	require.Equal(t, "abcdef123456", decoded.SHA256)
	require.Equal(t, int64(1024), decoded.Size)
}

type fakeCasStore struct {
	entities.CASStore
	objects map[entities.Hash][]byte
	maps    map[entities.Hash][]entities.Hash
}

func newFakeCasStore() *fakeCasStore {
	return &fakeCasStore{
		objects: make(map[entities.Hash][]byte),
		maps:    make(map[entities.Hash][]entities.Hash),
	}
}

func (f *fakeCasStore) GetObject(ctx context.Context, hash entities.Hash) ([]byte, error) {
	if b, ok := f.objects[hash]; ok {
		return b, nil
	}
	return nil, entities.NotExistErrf("object %s not found", hash)
}

func (f *fakeCasStore) MapResults(ctx context.Context, hash entities.Hash) ([]entities.Hash, error) {
	if res, ok := f.maps[hash]; ok {
		return res, nil
	}
	return nil, nil
}

func (f *fakeCasStore) RefreshMapResults(ctx context.Context, hash entities.Hash) error {
	return nil
}

func TestGetRBEDigest_FromDigestMap_Hit(t *testing.T) {
	tempDir := t.TempDir()
	content := []byte("hello digest map\n")
	filePath := filepath.Join(tempDir, "file.txt")
	require.NoError(t, os.WriteFile(filePath, content, 0644))

	gitSha1 := entities.NewHash("1111111111111111111111111111111111111111")
	// Distinct precomputed SHA-256 to prove it came from xxmap without reading file bytes
	precomputedSHA256 := entities.NewSHA256("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	mockFs := &mockCasfs{
		mountPoint: tempDir,
		hashes: map[string]entities.Hash{
			"file.txt": gitSha1,
		},
		sizes: map[string]int64{
			"file.txt": int64(len(content)),
		},
	}
	m := NewCasfsMount(nil)
	m.mountPoint = tempDir
	m.casfs = mockFs

	m.AddDigestMapEntries([]binding.DigestEntry{
		{Hash: gitSha1, SHA256: precomputedSHA256},
	})

	resp, err := m.GetRBEDigest(context.Background(), "file.txt")
	require.NoError(t, err)
	require.Equal(t, "file.txt", resp.Path)
	require.Equal(t, precomputedSHA256.String(), resp.SHA256)
	require.Equal(t, int64(len(content)), resp.Size)
	require.Equal(t, uint32(syscall.S_IFREG|0644), resp.Mode)
}

func TestGetRBEDigest_FromDigestMap_DirtyBypass(t *testing.T) {
	tempDir := t.TempDir()
	content := []byte("modified content\n")
	filePath := filepath.Join(tempDir, "file.txt")
	require.NoError(t, os.WriteFile(filePath, content, 0644))

	gitSha1 := entities.NewHash("1111111111111111111111111111111111111111")
	oldStaleSHA256 := entities.NewSHA256("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	mockFs := &mockCasfs{
		mountPoint: tempDir,
		hashes: map[string]entities.Hash{
			"file.txt": gitSha1,
		},
		sizes: map[string]int64{
			"file.txt": int64(len(content)),
		},
		dirtyMap: map[string]bool{
			"file.txt": true, // File is dirty!
		},
	}
	m := NewCasfsMount(nil)
	m.mountPoint = tempDir
	m.casfs = mockFs

	m.AddDigestMapEntries([]binding.DigestEntry{
		{Hash: gitSha1, SHA256: oldStaleSHA256},
	})

	// Expected SHA256 should be computed from the disk file since file is dirty
	h := sha256.Sum256(content)
	expectedActualSHA := hex.EncodeToString(h[:])

	resp, err := m.GetRBEDigest(context.Background(), "file.txt")
	require.NoError(t, err)
	require.Equal(t, expectedActualSHA, resp.SHA256)
	require.NotEqual(t, oldStaleSHA256.String(), resp.SHA256)
}

func TestGetRBEDigest_FromDigestMap_MissLearns(t *testing.T) {
	tempDir := t.TempDir()
	content := []byte("content to learn\n")
	filePath := filepath.Join(tempDir, "file.txt")
	require.NoError(t, os.WriteFile(filePath, content, 0644))

	gitSha1 := entities.NewHash("2222222222222222222222222222222222222222")

	mockFs := &mockCasfs{
		mountPoint: tempDir,
		hashes: map[string]entities.Hash{
			"file.txt": gitSha1,
		},
		sizes: map[string]int64{
			"file.txt": int64(len(content)),
		},
	}
	m := NewCasfsMount(nil)
	m.mountPoint = tempDir
	m.casfs = mockFs

	// 1. Initial lookup is a miss in digestMap -> calculates from disk
	h := sha256.Sum256(content)
	expectedSHA := hex.EncodeToString(h[:])

	resp, err := m.GetRBEDigest(context.Background(), "file.txt")
	require.NoError(t, err)
	require.Equal(t, expectedSHA, resp.SHA256)

	// 2. Verify that digestMap learned this hash
	learned := m.digestMap.Get(gitSha1.String())
	require.False(t, learned.IsZero())
	require.Equal(t, expectedSHA, learned.String())

	// 3. Now overwrite file content on disk with different bytes (without marking dirty)
	// Second lookup should hit xxmap and return the cached SHA-256 without re-reading file!
	require.NoError(t, os.WriteFile(filePath, []byte("changed bytes on disk!"), 0644))

	respCached, err := m.GetRBEDigest(context.Background(), "file.txt")
	require.NoError(t, err)
	require.Equal(t, expectedSHA, respCached.SHA256)
}

func TestCasfsMount_LoadTreeDigestMap(t *testing.T) {
	ctx := context.Background()
	store := newFakeCasStore()

	tree := entities.NewHash("3333333333333333333333333333333333333333")
	blob1 := entities.NewHash("4444444444444444444444444444444444444444")
	sha1 := entities.NewSHA256("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	blobChild := entities.NewHash("8888888888888888888888888888888888888888")
	shaChild := entities.NewSHA256("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")

	childDM := &binding.DigestMap{
		Version: binding.CurrentDigestMapVersion,
		Digests: []binding.DigestEntry{
			{Hash: blobChild, SHA256: shaChild},
		},
	}
	require.NoError(t, childDM.Encode())

	req := &binding.DigestMapRequest{
		Version: binding.CurrentDigestMapRequestVersion,
		Tree:    tree,
	}
	require.NoError(t, req.Encode())

	targetDM := &binding.DigestMap{
		Version: binding.CurrentDigestMapVersion,
		Digests: []binding.DigestEntry{
			{Hash: blob1, SHA256: sha1},
		},
		DigestMaps: []entities.Hash{childDM.Hash},
	}
	require.NoError(t, targetDM.Encode())

	store.maps[req.Hash] = []entities.Hash{targetDM.Hash}
	store.objects[targetDM.Hash] = targetDM.Encoded
	store.objects[childDM.Hash] = childDM.Encoded

	m := NewCasfsMount(nil)
	m.store = store

	// Loading tree populates digestMap and loadedDigestMaps
	require.NoError(t, m.LoadTreeDigestMap(ctx, tree))
	require.True(t, m.loadedDigestMaps.Has(targetDM.Hash))
	require.True(t, m.loadedDigestMaps.Has(childDM.Hash))

	val := m.digestMap.Get(blob1.String())
	require.False(t, val.IsZero())
	require.Equal(t, sha1, val)

	valChild := m.digestMap.Get(blobChild.String())
	require.False(t, valChild.IsZero())
	require.Equal(t, shaChild, valChild)

	// Remove childDM from store to prove a second tree referencing childDM skips loading it and its descendants
	delete(store.objects, childDM.Hash)

	tree2 := entities.NewHash("abababababababababababababababababababab")
	blob2 := entities.NewHash("cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd")
	sha2 := entities.NewSHA256("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	req2 := &binding.DigestMapRequest{
		Version: binding.CurrentDigestMapRequestVersion,
		Tree:    tree2,
	}
	require.NoError(t, req2.Encode())

	targetDM2 := &binding.DigestMap{
		Version: binding.CurrentDigestMapVersion,
		Digests: []binding.DigestEntry{
			{Hash: blob2, SHA256: sha2},
		},
		DigestMaps: []entities.Hash{childDM.Hash},
	}
	require.NoError(t, targetDM2.Encode())

	store.maps[req2.Hash] = []entities.Hash{targetDM2.Hash}
	store.objects[targetDM2.Hash] = targetDM2.Encoded

	require.NoError(t, m.LoadTreeDigestMap(ctx, tree2))
	require.Equal(t, sha2, m.digestMap.Get(blob2.String()))

	// Idempotent second load
	require.NoError(t, m.LoadTreeDigestMap(ctx, tree))

	// Non-existent tree is non-fatal
	missingTree := entities.NewHash("9999999999999999999999999999999999999999")
	require.NoError(t, m.LoadTreeDigestMap(ctx, missingTree))
}

func TestHTTP_DigestMapEndpoints(t *testing.T) {
	store := newFakeCasStore()

	tree := entities.NewHash("5555555555555555555555555555555555555555")
	blob1 := entities.NewHash("6666666666666666666666666666666666666666")
	sha1 := entities.NewSHA256("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")

	req := &binding.DigestMapRequest{
		Version: binding.CurrentDigestMapRequestVersion,
		Tree:    tree,
	}
	require.NoError(t, req.Encode())

	targetDM := &binding.DigestMap{
		Version: binding.CurrentDigestMapVersion,
		Digests: []binding.DigestEntry{
			{Hash: blob1, SHA256: sha1},
		},
	}
	require.NoError(t, targetDM.Encode())

	store.maps[req.Hash] = []entities.Hash{targetDM.Hash}
	store.objects[targetDM.Hash] = targetDM.Encoded

	m := NewCasfsMount(nil)
	m.store = store

	httpHandler := CreateHTTP(m, store)
	router := httpHandler.CreateRouter("/mnt")

	// 1. Test POST /mnt/load-tree-digest-map
	body, _ := json.Marshal(map[string]string{"tree": tree.String()})
	r := httptest.NewRequest(http.MethodPost, "/mnt/load-tree-digest-map", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	val := m.digestMap.Get(blob1.String())
	require.False(t, val.IsZero())
	require.Equal(t, sha1, val)

	// 2. Test POST /mnt/add-digest-map-entries
	blob2 := entities.NewHash("7777777777777777777777777777777777777777")
	sha2 := entities.NewSHA256("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	entriesBody, _ := json.Marshal(map[string]any{
		"entries": []binding.DigestEntry{
			{Hash: blob2, SHA256: sha2},
		},
	})
	r2 := httptest.NewRequest(http.MethodPost, "/mnt/add-digest-map-entries", bytes.NewBuffer(entriesBody))
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, r2)
	require.Equal(t, http.StatusOK, w2.Code)

	val2 := m.digestMap.Get(blob2.String())
	require.False(t, val2.IsZero())
	require.Equal(t, sha2, val2)
}
