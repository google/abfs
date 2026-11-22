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
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/ninja-go/build"
	"github.com/google/ninja-go/graph"
	"github.com/google/ninja-go/ninja"
	ninjaruntime "github.com/google/ninja-go/runtime"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago/ninjalog"
	"github.com/google/abfs/shared/entities"
)

type warnLog struct {
	mu    sync.Mutex
	warns []string
}

func (*warnLog) BuildStarted()                                  {}
func (*warnLog) BuildFinished()                                 {}
func (*warnLog) BuildStepStarted(int, string, string, []string) {}
func (*warnLog) BuildStepFinished(int, int, string)             {}
func (*warnLog) BuildStepCanceled(int)                          {}
func (*warnLog) PlanHasTotalSteps(int)                          {}
func (*warnLog) Printf(string, ...any)                          {}
func (*warnLog) Tracef(string, ...any)                          {}
func (*warnLog) Debugf(string, ...any)                          {}
func (*warnLog) Infof(string, ...any)                           {}
func (*warnLog) Errorf(string, ...any)                          {}
func (*warnLog) Close() error                                   { return nil }

func (l *warnLog) Warnf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(format, a...))
}

type countGraphBackend struct{}

func (countGraphBackend) GraphLoading() error                   { return nil }
func (countGraphBackend) GraphLoaded(*graph.Graph) error        { return nil }
func (countGraphBackend) GetDeps(*graph.Node) ([]string, error) { return nil, nil }

// loadCountGraph loads "build out/x: touch" for out/a, out/b, out/d and out/e, and
// out/result built from them, without the ninja-go parser, which races under -race.
func loadCountGraph(t *testing.T) *graph.Graph {
	t.Helper()
	ctx := context.WithValue(context.Background(), ninjaruntime.RunParamsKey, &ninjaruntime.RunParameters{
		CliFlags: &ninjaruntime.CliFlags{ConcurrencyLevel: 2},
	})
	ctx = context.WithValue(ctx, ninjaruntime.BackendKey, graph.GraphBackend(countGraphBackend{}))

	n := &ninja.NinjaFile{
		FileName: "build.ninja",
		Rules:    []*ninja.Rule{{Name: "touch", Command: "touch $out"}},
	}
	ins := []string{"out/a", "out/b", "out/d", "out/e"}
	for _, out := range ins {
		n.Builds = append(n.Builds, &ninja.Build{Outputs: []string{out}, RuleName: "touch"})
	}
	n.Builds = append(n.Builds, &ninja.Build{Outputs: []string{"out/result"}, RuleName: "touch", Inputs: ins})
	for _, b := range n.Builds {
		b.NinjaFile = n.FileName
	}
	require.NoError(t, ninja.ExpandNinja(ctx, n))
	files := ninja.NewFiles(n.FileName)
	files.AddFile(n.FileName, n)

	g := graph.NewGraph()
	require.NoError(t, g.Load(ctx, files, []string{"out/result"}, false))
	return g
}

// countStore is a cache store that holds a response for a request only once add was
// called for it.
type countStore struct {
	entities.CASStore
	mu      sync.Mutex
	results map[entities.Hash][]entities.Hash
	objects map[entities.Hash][]byte
}

func (s *countStore) add(t *testing.T, resp *binding.EvalResponse) {
	t.Helper()
	require.NoError(t, resp.Encode())
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[resp.EvalRequest] = append(s.results[resp.EvalRequest], resp.Hash)
	s.objects[resp.Hash] = resp.Encoded
}

func (s *countStore) MapResults(_ context.Context, id entities.Hash) ([]entities.Hash, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.results[id]), nil
}

func (*countStore) RefreshMapResults(context.Context, entities.Hash) error { return nil }

func (s *countStore) GetObject(_ context.Context, key entities.Hash) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.objects[key]; ok {
		return b, nil
	}
	return nil, entities.NotExistErrf("object %s", key)
}

// countClient serves countStore and applies every patch.
type countClient struct {
	abfsClient
	store *countStore
}

func (c countClient) Store() entities.CASStore { return c.store }
func (countClient) ApplyPatch(entities.Patch, entities.Patch, entities.DeletePatch, string) error {
	return nil
}

// countBackend runs the real ABFSBackend under the real engine with a fake cache store:
// out/a, out/b and out/result hit at the first check, out/d goes through the real cache
// check, finds no response there and hits at the delayed recheck in eval, out/e finds no
// response, and out/p goes through the real cache check. With the cache off no step is
// looked up. A dry run uses the real ApplyIfCached and ExecLocal.
type countBackend struct {
	*ABFSBackend
	t     *testing.T
	store *countStore
}

func (b countBackend) BuildInit(e *build.Engine) error { b.engine = e; return nil }
func (b countBackend) IsRemotable(*build.Step) bool    { return false }
func (b countBackend) CreateDirs(*build.Step) error    { return nil }
func (b countBackend) CreateRSP(*build.Step) error     { return nil }

func (b countBackend) ApplyIfCached(step *build.Step) (bool, error) {
	if b.dryRun {
		return b.ABFSBackend.ApplyIfCached(step)
	}
	switch target := step.Node.Targets[0]; {
	case target == "out/d", target == "out/p":
		return b.ABFSBackend.ApplyIfCached(step)
	case b.config.EvalNoCache, target == "out/e":
		b.evals.Put(step.Node, &Eval{step: step, req: &binding.EvalRequest{}, triedResps: map[entities.Hash]struct{}{}})
		return false, nil
	}
	b.steps.addStep(ninjalog.RunTypeCached, "", true, &binding.EvalResponse{})
	return true, nil
}

func (b countBackend) ExecLocal(ctx context.Context, step *build.Step) build.ExecResult {
	if b.dryRun {
		return b.ABFSBackend.ExecLocal(ctx, step)
	}
	eval, _ := b.evals.Get(step.Node)
	if step.Node.Targets[0] == "out/d" && !b.config.EvalNoCache {
		// The response arrives while the step waits for a slot, and the wait is long
		// enough for eval to look again.
		b.store.add(b.t, &binding.EvalResponse{
			EvalRequest: eval.req.Hash,
			WritePatch:  entities.Patch{{Path: "out/d", Mode: entities.FileModeRegular, State: entities.PatchEntryStateOut}},
		})
		eval.lastRefresh = time.Now().Add(-2 * recheckCacheMinDuration)
		return b.ABFSBackend.ExecLocal(ctx, step)
	}
	return build.ExecResult{Err: b.postExec(eval, &binding.EvalResponse{}, ninjalog.RunTypeLocal, nil)}
}

func TestFinalizeBuild_CacheCounts(t *testing.T) {
	tests := []struct {
		name        string
		cacheOff    bool
		dryRun      bool
		wantLine    string
		wantHits    uint32
		wantMisses  uint32
		wantPercent float64
		wantSteps   *binding.StepStats
	}{
		{
			name:        "cache on",
			wantLine:    "cache: hits: 4, misses: 1, ratio: 80.000, not looked up: 0",
			wantHits:    4,
			wantMisses:  1,
			wantPercent: 80,
			wantSteps: &binding.StepStats{
				RunTypes:    map[string]uint32{"cached": 3, "delayed-cached": 1, "local": 1},
				MissReasons: map[string]uint32{binding.CacheReasonNoResult: 1},
			},
		},
		{
			name:     "cache off",
			cacheOff: true,
			wantLine: "cache: hits: 0, misses: 0, ratio: 0.000, not looked up: 5",
			wantSteps: &binding.StepStats{
				RunTypes:    map[string]uint32{"local": 5},
				NotLookedUp: map[string]uint32{binding.CacheReasonOff: 5},
			},
		},
		{
			name:      "dry run",
			dryRun:    true,
			wantLine:  "cache: hits: 0, misses: 0, ratio: 0.000, not looked up: 5",
			wantSteps: &binding.StepStats{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, s := runCountBuild(t, loadCountGraph(t), tt.cacheOff, tt.dryRun)
			require.True(t, strings.HasSuffix(line, ", fail: 0, "+tt.wantLine), line)
			require.Equal(t, tt.wantHits, s.CacheHitCount)
			require.Equal(t, tt.wantMisses, s.CacheMissCount)
			require.InDelta(t, tt.wantPercent, s.CacheHitPercent, 1e-9)
			require.Equal(t, tt.wantSteps, s.Steps)
		})
	}
}

// runCountBuild builds g with countBackend and returns the "Evaluation took" line and the
// ninja stats file.
func runCountBuild(t *testing.T, g *graph.Graph, cacheOff, dryRun bool) (string, *binding.NinjaStats) {
	t.Helper()
	t.Chdir(t.TempDir())

	store := &countStore{results: map[entities.Hash][]entities.Hash{}, objects: map[entities.Hash][]byte{}}
	c := NewABFSBackend()
	c.abfs = countClient{store: store}
	c.config = &Configuration{EvalNoCache: cacheOff}
	c.dryRun = dryRun
	c.graph = g
	c.matchFiles = newMatchFiles("out")
	c.startHashFetchers()
	require.NoError(t, c.collectInitialHashes())
	c.logFile = ninjalog.New(".ninja_log")
	require.NoError(t, c.logFile.OpenForWrite())
	log := &warnLog{}
	c.log = log
	c.startTime = time.Now()

	params := ninjaruntime.NewRunParams()
	params.CliFlags = &ninjaruntime.CliFlags{ConcurrencyLevel: 2, DryRun: dryRun}
	params.Log = log
	params.CleanInspectorsCount, params.CacheCheckersCount, params.RemoteExecutorsCount = 2, 2, 1
	ctx := context.WithValue(context.Background(), ninjaruntime.RunParamsKey, params)
	ctx = context.WithValue(ctx, ninjaruntime.BackendKey, build.BuildBackend(countBackend{ABFSBackend: c, t: t, store: store}))
	require.NoError(t, build.NewEngine(ctx, g).Build(ctx))

	var line string
	for _, w := range log.warns {
		if strings.HasPrefix(w, "Evaluation took ") {
			line = w
		}
	}

	b, err := os.ReadFile(".ninja_stats.0")
	require.NoError(t, err)
	var s binding.NinjaStats
	require.NoError(t, json.Unmarshal(b, &s))
	return line, &s
}

func TestFinalizeBuild_PhonyOutputCounts(t *testing.T) {
	tests := []struct {
		name      string
		cacheOff  bool
		wantLine  string
		wantSteps *binding.StepStats
	}{
		{
			name:     "cache on",
			wantLine: "cache: hits: 2, misses: 0, ratio: 100.000, not looked up: 1",
			wantSteps: &binding.StepStats{
				RunTypes:    map[string]uint32{"cached": 2, "local": 1},
				NotLookedUp: map[string]uint32{binding.CacheReasonPhonyOutput: 1},
			},
		},
		{
			name:     "cache off",
			cacheOff: true,
			wantLine: "cache: hits: 0, misses: 0, ratio: 0.000, not looked up: 3",
			wantSteps: &binding.StepStats{
				RunTypes:    map[string]uint32{"local": 3},
				NotLookedUp: map[string]uint32{binding.CacheReasonOff: 2, binding.CacheReasonPhonyOutput: 1},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// out/p declares phony_output, as kati writes a .PHONY target with a recipe, and
			// out/a and out/result hit.
			g := loadTestGraph(t, testNinjaFile(
				&ninja.Build{Outputs: []string{"out/a"}, RuleName: "touch"},
				&ninja.Build{Outputs: []string{"out/p"}, RuleName: "touch", KeyValues: []*ninja.KeyValue{{Key: "phony_output", Value: "true"}}},
				resultBuild([]string{"out/a", "out/p"}, nil)), "out/result")
			line, s := runCountBuild(t, g, tt.cacheOff, false)
			require.True(t, strings.HasSuffix(line, ", fail: 0, "+tt.wantLine), line)
			require.Equal(t, tt.wantSteps, s.Steps)
		})
	}
}
