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

package ninjago

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/google/ninja-go/build"
	"github.com/google/ninja-go/graph"
	ninjaruntime "github.com/google/ninja-go/runtime"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/store"
)

type fakeStore struct {
	entities.CASStore
	maps       []entities.Hash
	mapErr     error
	refreshErr error
	getErr     error
	putErr     error
	mapped     []entities.Hash
}

func (s *fakeStore) MapResults(context.Context, entities.Hash) ([]entities.Hash, error) {
	return s.maps, s.mapErr
}

func (s *fakeStore) RefreshMapResults(context.Context, entities.Hash) error {
	return s.refreshErr
}

func (s *fakeStore) GetObject(context.Context, entities.Hash) ([]byte, error) {
	return nil, s.getErr
}

func (s *fakeStore) PutObject(context.Context, entities.Hash, []byte) error {
	return s.putErr
}

func (s *fakeStore) AddMapResult(_ context.Context, _, to entities.Hash) error {
	s.mapped = append(s.mapped, to)
	return nil
}

type testLogger struct {
	ninjaruntime.Logger
	warnings []string
}

func (l *testLogger) Warnf(format string, a ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, a...))
}

func (l *testLogger) Tracef(string, ...any) {}

func newCacheCheckBackend(s *fakeStore) (*ABFSBackend, *testLogger) {
	log := &testLogger{}
	c := NewABFSBackend()
	c.config = &Configuration{}
	c.store = s
	c.log = log
	return c, log
}

func newTestEval(t *testing.T) *Eval {
	t.Helper()
	req := &binding.EvalRequest{Targets: []string{"out/x"}, Command: "touch out/x"}
	require.NoError(t, req.Encode())
	return &Eval{
		step:       &build.Step{Node: &graph.Node{Targets: []string{"out/x"}}},
		req:        req,
		triedResps: make(map[entities.Hash]struct{}),
	}
}

var errDial = errors.New("dial unix http.socket: connect: resource temporarily unavailable")

func TestApplyIfCached_TransportErrorIsMiss(t *testing.T) {
	tests := []struct {
		name  string
		store *fakeStore
	}{
		{name: "map results", store: &fakeStore{mapErr: errDial}},
		{name: "refresh", store: &fakeStore{refreshErr: errDial}},
		{name: "server fetch", store: &fakeStore{refreshErr: errors.New("failed POST /fetch-map: 500 Internal Server Error")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, log := newCacheCheckBackend(tt.store)
			cached, err := c.applyIfCached(context.Background(), newTestEval(t), false)
			require.NoError(t, err)
			require.False(t, cached)
			require.Equal(t, int64(1), c.cacheErrors.Load())
			require.Len(t, log.warnings, 1)
		})
	}
}

func TestApplyIfCached_SkipsUnfetchableResponse(t *testing.T) {
	h1, h2 := entities.NewHash("1111111111111111111111111111111111111111"), entities.NewHash("2222222222222222222222222222222222222222")
	c, log := newCacheCheckBackend(&fakeStore{maps: []entities.Hash{h1, h2}, getErr: syscall.ENOENT})
	eval := newTestEval(t)

	cached, err := c.applyIfCached(context.Background(), eval, false)
	require.NoError(t, err)
	require.False(t, cached)
	require.Contains(t, eval.triedResps, h1)
	require.Contains(t, eval.triedResps, h2)
	require.Equal(t, int64(1), c.cacheErrors.Load())
	require.Len(t, log.warnings, 2)
}

func TestApplyIfCached_CountsStepOnce(t *testing.T) {
	c, _ := newCacheCheckBackend(&fakeStore{mapErr: errDial})
	eval := newTestEval(t)

	for _, delayed := range []bool{false, true} {
		cached, err := c.applyIfCached(context.Background(), eval, delayed)
		require.NoError(t, err)
		require.False(t, cached)
	}
	require.Equal(t, int64(1), c.cacheErrors.Load())
}

func TestRecordResults(t *testing.T) {
	notifyErr := fmt.Errorf("%w: %w", store.ErrProviderNotify, errors.New("client does not have an established connection"))
	tests := []struct {
		name       string
		putErr     error
		addMap     bool
		wantMapped int
		wantErr    bool
	}{
		{name: "stored and mapped", addMap: true, wantMapped: 1},
		{name: "stored, failed step", addMap: false},
		{name: "notify failed", putErr: notifyErr, addMap: true},
		{name: "local write failed", putErr: errors.New("no space left on device"), addMap: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &fakeStore{putErr: tt.putErr}
			c, _ := newCacheCheckBackend(s)
			resp := &binding.EvalResponse{Version: binding.CurrentEvalResponseVersion}
			err := c.recordResults(context.Background(), newTestEval(t), resp, tt.addMap)
			require.Len(t, s.mapped, tt.wantMapped)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			// The ninja log records resp.Hash, so it must be set even when the notify failed.
			require.False(t, resp.Hash.IsZero())
		})
	}
}
