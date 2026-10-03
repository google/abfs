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

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/rbe/model"
	"github.com/google/abfs/shared/remote"
)

type mockIdentityStore struct {
	mappings map[entities.Hash]model.Node
}

func (m *mockIdentityStore) Put(sha1 entities.Hash, node model.Node) error {
	m.mappings[sha1] = node
	return nil
}

func (m *mockIdentityStore) PutMany(entries []IdentityEntry) error {
	for _, e := range entries {
		m.mappings[e.SHA1] = e.Node
	}
	return nil
}

func (m *mockIdentityStore) Get(sha1 entities.Hash) (model.Node, bool, error) {
	d, ok := m.mappings[sha1]
	return d, ok, nil
}

func (m *mockIdentityStore) Close() error {
	return nil
}

type mockFallback struct {
	remote.RemoteProvider
	GetObjectFunc func(ctx context.Context, key entities.Hash) (remote.ContentReadCloser, error)
}

func (m *mockFallback) GetObject(ctx context.Context, key entities.Hash) (remote.ContentReadCloser, error) {
	if m.GetObjectFunc != nil {
		return m.GetObjectFunc(ctx, key)
	}
	return nil, entities.NotExistErrf("not found %s", key)
}

func (m *mockFallback) Headers(ctx context.Context, keys []entities.Hash) ([]entities.ObjectHeader, error) {
	return make([]entities.ObjectHeader, len(keys)), nil
}

func (m *mockFallback) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	return make([]entities.Hash, len(refs)), nil
}

// FIXME mockFallback is not used.
var _ mockFallback
