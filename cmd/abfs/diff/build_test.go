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

package diff

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago/ninjalog"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/entities"
)

type fakeObjectStore struct {
	entities.CASStore
	objects map[entities.Hash][]byte
}

func (f *fakeObjectStore) GetObject(ctx context.Context, hash entities.Hash) ([]byte, error) {
	if b, ok := f.objects[hash]; ok {
		return b, nil
	}
	return nil, entities.NotExistErrf("object %s not found", hash)
}

type encodable interface {
	Encode() error
	GetHash() entities.Hash
	GetEncoded() []byte
}

func (f *fakeObjectStore) put(t *testing.T, o encodable) entities.Hash {
	t.Helper()
	require.NoError(t, o.Encode())
	f.objects[o.GetHash()] = o.GetEncoded()
	return o.GetHash()
}

// writeBuildLog writes a ninjago log with one step that has the given request and response.
func writeBuildLog(t *testing.T, name, target string, req, resp entities.Hash) string {
	t.Helper()
	l := ninjalog.New(filepath.Join(t.TempDir(), name))
	require.NoError(t, l.OpenForWrite())
	require.NoError(t, l.WriteEntry(&ninjalog.LogEntry{
		Time:     time.Unix(1700000000, 0),
		Duration: time.Second,
		RunType:  ninjalog.RunTypeLocal,
		ReqHash:  req,
		RespHash: resp,
		Target:   target,
	}))
	require.NoError(t, l.Close())
	return l.Name()
}

func TestDiffBuild_FetchesFromProvidedStore(t *testing.T) {
	ctx := context.Background()
	store := &fakeObjectStore{objects: map[entities.Hash][]byte{}}

	// Two builds of one link step at different mount paths: same request, the written
	// link has a different target and so a different hash.
	req := store.put(t, &binding.EvalRequest{Command: "ln -sfn $PWD/src out/link", Targets: []string{"out/link"}})
	link := func(hash string) entities.Patch {
		return entities.Patch{{Path: "out/link", Mode: entities.FileModeSymlink, Hash: entities.NewHash(hash), Size: 20}}
	}
	respA := store.put(t, &binding.EvalResponse{EvalRequest: req, WritePatch: link("1111111111111111111111111111111111111111")})
	respB := store.put(t, &binding.EvalResponse{EvalRequest: req, WritePatch: link("2222222222222222222222222222222222222222")})
	logA := writeBuildLog(t, "a.ninja_log", "out/link", req, respA)
	logB := writeBuildLog(t, "b.ninja_log", "out/link", req, respB)

	// The command gets its store the way the CLI provides it to every command.
	c := NewBuildCmd(nil)
	require.NoError(t, cmd.SetEmbedded(c, func() (*cmd.StoreProvider, error) {
		return &cmd.StoreProvider{Store: store}, nil
	}))
	c.numResolvers = 2 // zero workers would block diffSection

	a, err := c.decode(logA)
	require.NoError(t, err)
	b, err := c.decode(logB)
	require.NoError(t, err)
	require.Len(t, a, 1)
	require.Len(t, b, 1)

	out := c.diffSection(ctx, a[0], b[0])
	require.Empty(t, out.errors)
	d, ok := out.diffs["out/link"]
	require.True(t, ok)
	require.True(t, d.isDiff())
	require.False(t, d.in.isDiff())
	require.Len(t, d.wr.changes, 1)
}
