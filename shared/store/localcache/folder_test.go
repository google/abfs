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

package localcache

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

var (
	hash1 = entities.NewHash("1a5bcb2a05bce51fa277c19f3aa4f42be2bb717b")
	hash2 = entities.NewHash("2581d6cba5ea2bd2b5c257037f4e007bf28de556")
	hash3 = entities.NewHash("340dcd4efec6fc364a9f0ffb4bde4351abfa5d93")
	hash4 = entities.NewHash("41862f73879c3b6a89852fa912e00aed642c7b69")
	hash5 = entities.NewHash("5c853f5bf200be12f2f983a1a8ad6712d967c236")
)

func TestFolder(t *testing.T) {
	f := NewFolder()
	require.NoError(t, f.Add("server0", hash1))
	assert.Equal(t, hash1, f.Get("server0"))
	assert.Zero(t, f.Get("server1"))

	require.NoError(t, f.Add("server1/b/c/d", hash2))
	assert.Equal(t, hash2, f.Get("server1/b/c/d"))
	assert.Zero(t, f.Get("server1/b/c"))
	assert.Zero(t, f.Get("server0/b/c"))

	require.NoError(t, f.Add("server1/b/c/d2", hash3))
	assert.Equal(t, hash3, f.Get("server1/b/c/d2"))
	require.NoError(t, f.Add("server1/b/c/d2", hash4))
	assert.Equal(t, hash4, f.Get("server1/b/c/d2"))

	require.NoError(t, f.Add("server2", hash3))
	require.ErrorIs(t, f.Add("server2/a", hash4), syscall.ENOTDIR)
	assert.Equal(t, hash3, f.Get("server2"))
	require.ErrorIs(t, f.Add("server1/b/c", hash4), syscall.EISDIR)

	assert.Equal(t, map[string]entities.Hash{
		"server0":        hash1,
		"server1/b/c/d":  hash2,
		"server1/b/c/d2": hash4,
		"server2":        hash3,
	}, f.GetChildren(""))
	assert.Equal(t, map[string]entities.Hash{"server0": hash1}, f.GetChildren("server0"))
	assert.Equal(t, map[string]entities.Hash{"server1/b/c/d": hash2}, f.GetChildren("server1/b/c/d"))
	assert.Equal(t, map[string]entities.Hash{
		"server1/b/c/d":  hash2,
		"server1/b/c/d2": hash4,
	}, f.GetChildren("server1"))
	assert.Equal(t, map[string]entities.Hash{
		"server1/b/c/d":  hash2,
		"server1/b/c/d2": hash4,
	}, f.GetChildren("server1/b"))
	require.NoError(t, f.Add("server1/b/e", hash1))
	assert.Equal(t, map[string]entities.Hash{
		"server1/b/c/d":  hash2,
		"server1/b/c/d2": hash4,
		"server1/b/e":    hash1,
	}, f.GetChildren("server1/b"))
}
