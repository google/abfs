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

package grpc

import (
	"context"
	"errors"
	"time"

	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
)

// MockBlockingRemoteCloser implements remote.RemoteCloser for testing. It blocks on GetObject until the context is cancelled to test context cancellation.
type MockBlockingRemoteCloser struct {
	remote.RemoteProvider
	blockCh  chan struct{}
	cancelCh chan struct{}
}

func (m *MockBlockingRemoteCloser) Close() error {
	return nil
}
func (m *MockBlockingRemoteCloser) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	// Return a placeholder hash so resolution succeeds
	// We return 1 hash for 1 ref
	hashes := make([]entities.Hash, len(refs))
	for i := range refs {
		// Just use a random valid-looking hash. ZeroHash doesn't work here.
		hashes[i] = entities.NewHash("0000000000000000000000000000000000000001")
	}
	return hashes, nil
}

// GetObject blocks to verify context cancellation
func (m *MockBlockingRemoteCloser) GetObject(ctx context.Context, key entities.Hash) (remote.ContentReadCloser, error) {
	select {
	case m.blockCh <- struct{}{}:
	default:
	}

	select {
	case <-ctx.Done():
		close(m.cancelCh)
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return nil, errors.New("timeout waiting for context cancel")
	}
}

// Minimal implementations for other methods
func (m *MockBlockingRemoteCloser) Headers(ctx context.Context, keys []entities.Hash) ([]entities.ObjectHeader, error) {
	return nil, nil
}
func (m *MockBlockingRemoteCloser) PutObject(ctx context.Context, key entities.Hash, reader remote.ContentReadCloser) error {
	return nil
}
func (m *MockBlockingRemoteCloser) MapResults(ctx context.Context, from entities.Hash) ([]entities.Hash, error) {
	return nil, nil
}
func (m *MockBlockingRemoteCloser) AddMapResult(ctx context.Context, from, to entities.Hash) error {
	return nil
}
func (m *MockBlockingRemoteCloser) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return nil, nil
}
func (m *MockBlockingRemoteCloser) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	return nil
}
func (m *MockBlockingRemoteCloser) Mark(ctx context.Context, hash entities.Hash, key, value string) (int64, error) {
	return 0, nil
}
func (m *MockBlockingRemoteCloser) Version(ctx context.Context) (string, error) {
	return "", nil
}

// MockConfigRemoteCloser implements remote.RemoteCloser for testing ARESServerConfig config loading in RandomConnMgr.
type MockConfigRemoteCloser struct {
	remote.RemoteProvider
	config      ARESServerConfig
	configReady chan struct{}

	// Precomputed hashes
	configHash     entities.Hash
	serverTreeHash entities.Hash
	rootTreeHash   entities.Hash

	// Precomputed content
	configBlob     []byte
	serverTreeBlob []byte
	rootTreeBlob   []byte
}

func NewMockConfigRemoteCloser(config ARESServerConfig) (*MockConfigRemoteCloser, error) {
	// 1. Serialize Config
	configData, err := yaml.Marshal(config)
	if err != nil {
		return nil, err
	}

	m := &MockConfigRemoteCloser{
		config:         config,
		configReady:    make(chan struct{}, 1),
		configHash:     entities.NewHash("1111111111111111111111111111111111111111"), // Arbitrary
		serverTreeHash: entities.NewHash("2222222222222222222222222222222222222222"), // Arbitrary
		rootTreeHash:   entities.NewHash("3333333333333333333333333333333333333333"), // Arbitrary
	}

	// 1. Config Blob
	header := fmt.Sprintf("blob %d\x00", len(configData))
	m.configBlob = append([]byte(header), configData...)

	// 2. Server Tree (contains "ares" -> configHash)
	// Entry: "100644 ares\0" + configHash
	var serverTreeBuf bytes.Buffer
	serverTreeBuf.WriteString("100644 ares\x00")
	serverTreeBuf.Write(m.configHash[:])
	serverTreeData := serverTreeBuf.Bytes()
	serverTreeHeader := fmt.Sprintf("tree %d\x00", len(serverTreeData))
	m.serverTreeBlob = append([]byte(serverTreeHeader), serverTreeData...)

	// 3. Root Tree (contains "server" -> serverTreeHash)
	// Entry: "40000 server\0" + serverTreeHash
	var rootTreeBuf bytes.Buffer
	rootTreeBuf.WriteString("40000 server\x00")
	rootTreeBuf.Write(m.serverTreeHash[:])
	rootTreeData := rootTreeBuf.Bytes()
	rootTreeHeader := fmt.Sprintf("tree %d\x00", len(rootTreeData))
	m.rootTreeBlob = append([]byte(rootTreeHeader), rootTreeData...)

	return m, nil
}

func (m *MockConfigRemoteCloser) Close() error {
	return nil
}

func (m *MockConfigRemoteCloser) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	// Return Root Tree Hash. hashpath will walk "server/ares" from here.
	return []entities.Hash{m.rootTreeHash}, nil
}

func (m *MockConfigRemoteCloser) GetObject(ctx context.Context, key entities.Hash) (remote.ContentReadCloser, error) {
	select {
	case m.configReady <- struct{}{}:
	default:
	}

	if key == m.rootTreeHash {
		return remote.NewContentReader(io.NopCloser(bytes.NewReader(m.rootTreeBlob)), "raw"), nil
	}
	if key == m.serverTreeHash {
		return remote.NewContentReader(io.NopCloser(bytes.NewReader(m.serverTreeBlob)), "raw"), nil
	}
	if key == m.configHash {
		return remote.NewContentReader(io.NopCloser(bytes.NewReader(m.configBlob)), "raw"), nil
	}

	return nil, fmt.Errorf("not found: %s", key)
}

// Minimal implementations for other methods
func (m *MockConfigRemoteCloser) Headers(ctx context.Context, keys []entities.Hash) ([]entities.ObjectHeader, error) {
	return nil, nil
}
func (m *MockConfigRemoteCloser) PutObject(ctx context.Context, key entities.Hash, reader remote.ContentReadCloser) error {
	return nil
}
func (m *MockConfigRemoteCloser) MapResults(ctx context.Context, from entities.Hash) ([]entities.Hash, error) {
	return nil, nil
}
func (m *MockConfigRemoteCloser) AddMapResult(ctx context.Context, from, to entities.Hash) error {
	return nil
}
func (m *MockConfigRemoteCloser) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return nil, nil
}
func (m *MockConfigRemoteCloser) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	return nil
}
func (m *MockConfigRemoteCloser) Mark(ctx context.Context, hash entities.Hash, key, value string) (int64, error) {
	return 0, nil
}
func (m *MockConfigRemoteCloser) Version(ctx context.Context) (string, error) {
	return "", nil
}
