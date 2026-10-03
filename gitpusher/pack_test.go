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

package gitpusher

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
)

func TestTimeToGroup(t *testing.T) {
	// Reference time: 2025-05-17 12:23:34 UTC
	// Year: 2025
	// Month: May (5) -> Q2 (Jan-Mar=Q1, Apr-Jun=Q2)
	// Day: 17 -> Block (17-1)/5 = 3 (Days 16-20)
	now := time.Date(2025, time.May, 17, 12, 23, 34, 456, time.UTC)

	tests := []struct {
		name     string
		t        time.Time
		expected time.Time
	}{
		{
			name:     "Future",
			t:        now.Add(24 * time.Hour),
			expected: now.Add(24 * time.Hour), // Fallback to "End of Today"
		},
		{
			name:     "Today",
			t:        now.Add(-2 * time.Hour),
			expected: now.Add(-2 * time.Hour),
		},
		{
			name:     "Yesterday (Same block)",
			t:        time.Date(2025, time.May, 16, 10, 0, 0, 0, time.UTC),
			expected: time.Date(2025, time.May, 16, 23, 59, 59, 0, time.UTC),
		},
		{
			name:     "Same Month, Older Block",
			t:        time.Date(2025, time.May, 3, 10, 0, 0, 0, time.UTC),
			expected: time.Date(2025, time.May, 4, 23, 59, 59, 0, time.UTC),
		},
		{
			name:     "Same Quarter, Older Month",
			t:        time.Date(2025, time.April, 15, 10, 0, 0, 0, time.UTC),
			expected: time.Date(2025, time.April, 30, 23, 59, 59, 0, time.UTC),
		},
		{
			name:     "Older Quarter",
			t:        time.Date(2025, time.February, 15, 10, 0, 0, 0, time.UTC),
			expected: time.Date(2025, time.February, 28, 23, 59, 59, 0, time.UTC),
		},
		{
			name:     "Older Year",
			t:        time.Date(2024, time.December, 15, 10, 0, 0, 0, time.UTC),
			expected: time.Date(2024, time.December, 31, 23, 59, 59, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := timeToGroup(tc.t, now)
			assert.Equal(t, tc.expected, got)
		})
	}
}

// MockGit

type MockGit struct {
	mock.Mock
}

func (m *MockGit) RevListAll(repoPath string) ([]entities.Hash, error) {
	args := m.Called(repoPath)
	return args.Get(0).([]entities.Hash), args.Error(1)
}

func (m *MockGit) AllObjects(repoPath string) ([]entities.Hash, error) {
	args := m.Called(repoPath)
	return args.Get(0).([]entities.Hash), args.Error(1)
}

func (m *MockGit) PackObjects(repoPath string, prefix string, objects []entities.Hash) (string, error) {
	args := m.Called(repoPath, prefix, objects)
	if fn, ok := args.Get(0).(func(string, string, []entities.Hash) string); ok {
		return fn(repoPath, prefix, objects), args.Error(1)
	}
	return args.String(0), args.Error(1)
}

func (m *MockGit) AllCommitsDates(repoPath string) ([]entities.Hash, []time.Time, error) {
	args := m.Called(repoPath)
	return args.Get(0).([]entities.Hash), args.Get(1).([]time.Time), args.Error(2)
}

func (m *MockGit) CommitTreeHashes(root string, commits []entities.Hash) ([]entities.Hash, error) {
	args := m.Called(root, commits)
	return args.Get(0).([]entities.Hash), args.Error(1)
}

func (m *MockGit) CommitsFileObjects(root string, commits []entities.Hash) ([]entities.Hash, error) {
	args := m.Called(root, commits)
	return args.Get(0).([]entities.Hash), args.Error(1)
}

func (m *MockGit) ObjectTypes(repoPath string, objects []entities.Hash) ([]string, error) {
	args := m.Called(repoPath, objects)
	return args.Get(0).([]string), args.Error(1)
}

// MockRemoteProvider

type MockRemoteProvider struct {
	mock.Mock
}

func (m *MockRemoteProvider) Headers(ctx context.Context, keys []entities.Hash) ([]entities.ObjectHeader, error) {
	args := m.Called(ctx, keys)
	return args.Get(0).([]entities.ObjectHeader), args.Error(1)
}

func (m *MockRemoteProvider) SHA256(ctx context.Context, keys []entities.Hash) ([]entities.SHA256, error) {
	args := m.Called(ctx, keys)
	return args.Get(0).([]entities.SHA256), args.Error(1)
}

func (m *MockRemoteProvider) GetObject(ctx context.Context, key entities.Hash) (remote.ContentReadCloser, error) {
	args := m.Called(ctx, key)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	if fn, ok := args.Get(0).(func(context.Context, entities.Hash) remote.ContentReadCloser); ok {
		return fn(ctx, key), args.Error(1)
	}
	return args.Get(0).(remote.ContentReadCloser), args.Error(1)
}

func (m *MockRemoteProvider) PutObject(ctx context.Context, key entities.Hash, reader remote.ContentReadCloser) error {
	// Consume reader to prevent leaks/deadlocks if any, though in mock we just return.
	// Real implementation calls reader.Close potentially or reads it.
	// We should simulate reading if we want to be nice, but Discard is fine.
	_, _ = io.Copy(io.Discard, reader)
	_ = reader.Close()
	args := m.Called(ctx, key, reader)
	return args.Error(0)
}

func (m *MockRemoteProvider) MapResults(ctx context.Context, from entities.Hash) ([]entities.Hash, error) {
	args := m.Called(ctx, from)
	return args.Get(0).([]entities.Hash), args.Error(1)
}

func (m *MockRemoteProvider) AddMapResult(ctx context.Context, from, to entities.Hash) error {
	args := m.Called(ctx, from, to)
	return args.Error(0)
}

func (m *MockRemoteProvider) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	args := m.Called(ctx, server, project, refs)
	return args.Get(0).([]entities.Hash), args.Error(1)
}

func (m *MockRemoteProvider) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	args := m.Called(ctx, server, project, refPrefix, limit)
	return args.Get(0).([]entities.ProjectRefHash), args.Error(1)
}

func (m *MockRemoteProvider) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	args := m.Called(ctx, server, project, refs)
	return args.Error(0)
}

func (m *MockRemoteProvider) Mark(ctx context.Context, hash entities.Hash, key, value string) (int64, error) {
	args := m.Called(ctx, hash, key, value)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockRemoteProvider) Version(ctx context.Context) (string, error) {
	args := m.Called(ctx)
	return args.String(0), args.Error(1)
}

func (m *MockRemoteProvider) WhoAmI(ctx context.Context) (string, error) {
	args := m.Called(ctx)
	return args.String(0), args.Error(1)
}

func (m *MockRemoteProvider) Close() error {
	return nil
}

// Helper to create a fake pack file on disk
func createPackFile(t *testing.T, prefix string) string {
	err := os.WriteFile(prefix+"-test.pack", []byte("pack content"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(prefix+"-test.idx", []byte("index content"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	return prefix + "-test"
}

func generateValidIndex() []byte {
	// We need to construct a minimal valid Git v2 Pack Index.
	// Header: \xfftOc \x00\x00\x00\x02
	// Fanout: 256 * 4 bytes
	// Hashes: N * 20 bytes
	// CRCs: N * 4 bytes
	// Offsets: N * 4 bytes
	// PackChecksum: 20 bytes
	// IndexChecksum: 20 bytes
	//
	// N=0 is simplest.
	//
	buf := new(bytes.Buffer)
	buf.Write([]byte{0xff, 0x74, 0x4f, 0x63, 0x00, 0x00, 0x00, 0x02}) // Header
	for i := 0; i < 256; i++ {
		buf.Write([]byte{0, 0, 0, 0}) // Fanout (0 objects)
	}
	// No hashes, no crcs, no offsets.
	buf.Write(make([]byte, 20)) // Pack Checksum (placeholder)
	buf.Write(make([]byte, 20)) // Index Checksum (placeholder)
	return buf.Bytes()
}

func TestPackPusher_Push_FreshStart(t *testing.T) {
	ctx := context.Background()
	gitRoot := t.TempDir()
	server := "server"
	project := "project"

	mockGit := new(MockGit)
	mockRemote := new(MockRemoteProvider)

	mockRemote.On("GetRefs", ctx, server, project, []string{binding.PacksRefs}).Return([]entities.Hash{entities.ZeroHash}, nil).Twice()
	mockRemote.On("GetObject", ctx, entities.ZeroHash).Return(nil, entities.ErrNotExist).Twice()

	commits := []entities.Hash{{0x1}, {0x2}, {0x3}}
	dates := []time.Time{time.Now(), time.Now(), time.Now()}
	mockGit.On("AllCommitsDates", gitRoot).Return(commits, dates, nil)

	mockGit.On("PackObjects", gitRoot, mock.Anything, commits).Return(func(repoPath, prefix string, objects []entities.Hash) string {
		return createPackFile(t, prefix)
	}, nil)

	mockRemote.On("PutObject", ctx, mock.Anything, mock.Anything).Return(nil).Times(3) // 2 pack files + 1 manifest
	mockRemote.On("PutRefs", ctx, server, project, mock.Anything).Return(nil).Once()

	pusher, err := NewPackPusher(mockRemote, server, project, gitRoot, mockGit, binding.PackingStrategyCommits)
	assert.NoError(t, err)

	err = pusher.Push(ctx)
	assert.NoError(t, err)

	mockGit.AssertExpectations(t)
	mockRemote.AssertExpectations(t)
}

func TestPackPusher_Push_Incremental(t *testing.T) {
	ctx := context.Background()
	gitRoot := t.TempDir()
	server := "server"
	project := "project"

	mockGit := new(MockGit)
	mockRemote := new(MockRemoteProvider)

	// EXISTING PACKS SETUP
	now := time.Now().UTC()
	// Use explicit dates to ensure they fall on the same day/period and thus get grouped together
	packID1 := "existing-pack-1"
	packTime1 := time.Date(now.Year(), now.Month(), now.Day()-2, 12, 0, 0, 0, time.UTC)
	packID2 := "existing-pack-2"
	packTime2 := time.Date(now.Year(), now.Month(), now.Day()-2, 10, 0, 0, 0, time.UTC)

	packContent := []byte("pack content")
	idxContent := generateValidIndex()

	existingPack1 := binding.PackEntry{
		Type:         binding.CommitsPack,
		PackID:       packID1,
		PackedTime:   packTime1,
		ObjectsCount: 0, // 0 objects in index to match generateValidIndex
		Content:      entities.Hash{0xAA},
		ContentSize:  int64(len(packContent)),
		Index:        entities.Hash{0xBB},
		IndexSize:    int64(len(idxContent)),
	}
	existingPack2 := binding.PackEntry{
		Type:         binding.CommitsPack,
		PackID:       packID2,
		PackedTime:   packTime2,
		ObjectsCount: 0,
		Content:      entities.Hash{0xCC},
		ContentSize:  int64(len(packContent)),
		Index:        entities.Hash{0xDD},
		IndexSize:    int64(len(idxContent)),
	}
	packs := &binding.PackFiles{
		Version:        binding.CurrentPackFilesVersion,
		Strategy:       binding.PackingStrategyCommits,
		LastUpdateTime: time.Now(),
		Packs:          []binding.PackEntry{existingPack1, existingPack2},
	}
	require.NoError(t, packs.Encode())
	packsHash := packs.Hash

	// Expectations for init()
	// FetchPacks -> GetRefs -> packsHash
	mockRemote.On("GetRefs", ctx, server, project, []string{binding.PacksRefs}).Return([]entities.Hash{packsHash}, nil).Twice()

	// GetObject(packsHash) -> returns packs
	// Return a NEW reader each time
	mockRemote.On("GetObject", ctx, packsHash).Return(func(ctx context.Context, h entities.Hash) remote.ContentReadCloser {
		return remote.NewReaderFromBytes(packs.Encoded)
	}, nil).Twice()

	// PackPusher.init -> cleanPackRoot -> downloads existing packs
	mockRemote.On("GetObject", ctx, existingPack1.Content).Return(func(_ context.Context, _ entities.Hash) remote.ContentReadCloser {
		return remote.NewBlobReaderFromBytes(packContent)
	}, nil).Once()

	mockRemote.On("GetObject", ctx, existingPack1.Index).Return(func(_ context.Context, _ entities.Hash) remote.ContentReadCloser {
		return remote.NewBlobReaderFromBytes(idxContent)
	}, nil).Once()

	mockRemote.On("GetObject", ctx, existingPack2.Content).Return(func(_ context.Context, _ entities.Hash) remote.ContentReadCloser {
		return remote.NewBlobReaderFromBytes(packContent)
	}, nil).Once()

	mockRemote.On("GetObject", ctx, existingPack2.Index).Return(func(_ context.Context, _ entities.Hash) remote.ContentReadCloser {
		return remote.NewBlobReaderFromBytes(idxContent)
	}, nil).Once()

	// packMissings calls RevListAll
	newObjects := make([]entities.Hash, 150)
	for i := 0; i < 150; i++ {
		newObjects[i] = entities.Hash{byte(i + 10)}
	}
	mockGit.On("RevListAll", gitRoot).Return(newObjects, nil)

	// Calls PackObjects for NEW objects
	mockGit.On("PackObjects", gitRoot, mock.Anything, newObjects).Return(func(repoPath, prefix string, objects []entities.Hash) string {
		return createPackFile(t, prefix)
	}, nil)

	// Calls AllObjects for compaction - assume empty to keep everything
	mockGit.On("AllObjects", gitRoot).Return([]entities.Hash{}, nil)

	// Calls PackObjects for MERGED objects
	// We expect merged objects to be (newObjects + existingObjects).
	// Since existingObjects=0 (empty valid index), expecting just newObjects (150).
	// However, the test logic calls `addPackObjects`.
	// Since index is empty, objects map is empty.
	// So only `newObjects` are packed again?
	// Wait, code:
	// "combinedObjects = append(combinedObjects, h)" where h is in existing packs.
	// existing packs have 0 objects.
	// So combinedObjects = [].
	// AND we are merging `packs` which is [existingPack, newPack].
	// newPack has 150 objects.
	// So combinedObjects = objects from newPack.
	// So we expect PackObjects call with `newObjects` again (as merged pack).
	mockGit.On("PackObjects", gitRoot, mock.Anything, mock.Anything).Return(func(repoPath, prefix string, objects []entities.Hash) string {
		return createPackFile(t, prefix)
	}, nil)

	// PutObject calls:
	// 1. Upload new pack (2 calls: .pack, .idx)
	// 2. Upload merged pack (2 calls: .pack, .idx)
	// 3. Upload manifest (1 call)
	mockRemote.On("PutObject", ctx, mock.Anything, mock.Anything).Return(nil).Times(5)

	mockRemote.On("PutRefs", ctx, server, project, mock.Anything).Return(nil).Once()

	pusher, err := NewPackPusher(mockRemote, server, project, gitRoot, mockGit, binding.PackingStrategyCommits)
	assert.NoError(t, err)

	err = pusher.Push(ctx)
	assert.NoError(t, err)

	mockGit.AssertExpectations(t)
	mockRemote.AssertExpectations(t)
}
