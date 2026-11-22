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
	"testing"

	"github.com/google/ninja-go/build"
	"github.com/google/ninja-go/graph"
	"github.com/google/ninja-go/ninja"
	ninjaruntime "github.com/google/ninja-go/runtime"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/entities"
)

type testGraphBackend struct{}

func (testGraphBackend) GraphLoading() error                   { return nil }
func (testGraphBackend) GraphLoaded(*graph.Graph) error        { return nil }
func (testGraphBackend) GetDeps(*graph.Node) ([]string, error) { return nil, nil }

// loadTestGraph loads n without the ninja-go parser, which races under -race at the
// pinned version (parse.ParseNinjaFile reads the loop's byte offset from its goroutines).
func loadTestGraph(t *testing.T, n *ninja.NinjaFile, goal string) *graph.Graph {
	t.Helper()
	ctx := context.WithValue(context.Background(), ninjaruntime.RunParamsKey, &ninjaruntime.RunParameters{
		CliFlags: &ninjaruntime.CliFlags{ConcurrencyLevel: 2},
	})
	ctx = context.WithValue(ctx, ninjaruntime.BackendKey, graph.GraphBackend(testGraphBackend{}))

	n.FileName = "build.ninja"
	for _, b := range n.Builds {
		b.NinjaFile = n.FileName
	}
	require.NoError(t, ninja.ExpandNinja(ctx, n))
	files := ninja.NewFiles(n.FileName)
	files.AddFile(n.FileName, n)

	g := graph.NewGraph()
	require.NoError(t, g.Load(ctx, files, []string{goal}, false))
	return g
}

// newTestBackend sets up the parts of the backend that toEvalRequest and
// remoteOrderOnlyPatch need, then gives srcs the hash a casfs mount would report for an
// empty file.
func newTestBackend(t *testing.T, g *graph.Graph, config *Configuration, srcs ...string) *ABFSBackend {
	t.Helper()
	t.Chdir(t.TempDir())

	c := NewABFSBackend()
	c.config = config
	c.graph = g
	c.matchFiles = newMatchFiles("out")
	c.startHashFetchers()
	t.Cleanup(func() {
		close(c.hashFetchC)
		c.pendingJobs.Wait()
	})
	require.NoError(t, c.collectInitialHashes())

	for _, p := range srcs {
		c.hashes.Put(p, &HashStat{Hash: entities.EmptyFileHash, Mode: entities.FileModeRegular})
	}
	return c
}

func loadedStep(g *graph.Graph, target string) *build.Step {
	step := build.NewStep(g.Targets.Get(target))
	step.LoadCommand()
	return step
}

// testNinjaFile returns a ninja file with these rules and builds:
//
//	rule check
//	  command = true
//	rule check_phony_output
//	  command = true
//	  phony_output = true
//	rule touch
//	  command = touch $out
func testNinjaFile(builds ...*ninja.Build) *ninja.NinjaFile {
	return &ninja.NinjaFile{
		Rules: []*ninja.Rule{
			{Name: "check", Command: "true"},
			{Name: "check_phony_output", Command: "true", PhonyOutput: "true"},
			{Name: "touch", Command: "touch $out"},
		},
		Builds: builds,
	}
}

// phonyOutputBuild returns "build out: check ins" with "phony_output = true" on the
// build, as kati writes a .PHONY target with a recipe.
func phonyOutputBuild(out string, ins ...string) *ninja.Build {
	return &ninja.Build{
		Outputs:   []string{out},
		RuleName:  "check",
		Inputs:    ins,
		KeyValues: []*ninja.KeyValue{{Key: "phony_output", Value: "true"}},
	}
}

func resultBuild(ins, orderOnlys []string) *ninja.Build {
	return &ninja.Build{
		Outputs:         []string{"out/result"},
		RuleName:        "touch",
		Inputs:          ins,
		OrderOnlyInputs: orderOnlys,
	}
}

func TestToEvalRequest_PhonyOutputInput(t *testing.T) {
	tests := []struct {
		name          string
		file          *ninja.NinjaFile
		config        *Configuration
		wantOrderOnly bool
	}{
		{
			name: "phony",
			file: testNinjaFile(
				&ninja.Build{Outputs: []string{"qssi_violators"}, RuleName: "phony", Inputs: []string{"src/rules.txt"}},
				resultBuild([]string{"qssi_violators"}, nil)),
		},
		{
			name: "phony_output on the build",
			file: testNinjaFile(
				phonyOutputBuild("qssi_violators", "src/rules.txt"),
				resultBuild([]string{"qssi_violators"}, nil)),
		},
		{
			name: "phony_output on the rule",
			file: testNinjaFile(
				&ninja.Build{Outputs: []string{"qssi_violators"}, RuleName: "check_phony_output", Inputs: []string{"src/rules.txt"}},
				resultBuild([]string{"qssi_violators"}, nil)),
		},
		{
			name: "phony_output through phony_output and phony",
			file: testNinjaFile(
				&ninja.Build{Outputs: []string{"rules"}, RuleName: "phony", Inputs: []string{"src/rules.txt"}},
				phonyOutputBuild("rules_check", "rules"),
				phonyOutputBuild("qssi_violators", "rules_check"),
				resultBuild([]string{"qssi_violators"}, nil)),
		},
		{
			name: "order-only phony_output",
			file: testNinjaFile(
				phonyOutputBuild("qssi_violators", "src/rules.txt"),
				resultBuild(nil, []string{"qssi_violators"})),
			wantOrderOnly: true,
		},
		{
			name: "order-only phony_output as regular input",
			file: testNinjaFile(
				phonyOutputBuild("qssi_violators", "src/rules.txt"),
				resultBuild(nil, []string{"qssi_violators"})),
			config:        &Configuration{OrderOnlyAsRegularInput: true},
			wantOrderOnly: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := tt.config
			if config == nil {
				config = &Configuration{}
			}
			g := loadTestGraph(t, tt.file, "out/result")
			c := newTestBackend(t, g, config, "src/rules.txt")

			req, err := c.toEvalRequest(loadedStep(g, "out/result"))
			require.NoError(t, err)

			require.Len(t, req.InputPatch, 1)
			require.Equal(t, "src/rules.txt", req.InputPatch[0].Path)
			require.Equal(t, tt.wantOrderOnly, req.InputPatch[0].State.IsOrderOnly())
		})
	}
}

func TestRemoteOrderOnlyPatch_PhonyOutputInput(t *testing.T) {
	g := loadTestGraph(t, testNinjaFile(
		phonyOutputBuild("qssi_violators", "src/rules.txt"),
		resultBuild(nil, []string{"qssi_violators"})), "out/result")
	c := newTestBackend(t, g, &Configuration{}, "src/rules.txt")

	h, err := c.remoteOrderOnlyPatch(context.Background(), &Eval{step: loadedStep(g, "out/result")})
	require.NoError(t, err)
	require.Equal(t, entities.ZeroHash, h)
}

const nilStorePanic = "runtime error: invalid memory address or nil pointer dereference"

func TestPhonyOutputStep_SkipsCache(t *testing.T) {
	g := loadTestGraph(t, testNinjaFile(
		phonyOutputBuild("qssi_violators", "src/rules.txt"),
		resultBuild([]string{"qssi_violators"}, nil)), "out/result")

	tests := []struct {
		name      string
		target    string
		wantStore bool
	}{
		{name: "phony_output step", target: "qssi_violators"},
		{name: "regular step", target: "out/result", wantStore: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestBackend(t, g, &Configuration{}, "src/rules.txt")
			// binding.Client takes its store only from NewClient, which needs a mounted
			// workspace. A zero Client has a nil store, so every store call panics.
			c.abfs = &binding.Client{}
			eval := &Eval{
				step:       loadedStep(g, tt.target),
				req:        &binding.EvalRequest{},
				triedResps: make(map[entities.Hash]struct{}),
			}

			for _, delayed := range []bool{false, true} {
				lookup := func() {
					cached, err := c.applyIfCached(context.Background(), eval, delayed)
					require.NoError(t, err)
					require.False(t, cached)
				}
				if tt.wantStore {
					require.PanicsWithError(t, nilStorePanic, lookup)
				} else {
					require.NotPanics(t, lookup)
				}
			}

			record := func() {
				c.addMapResult(context.Background(), eval, &binding.EvalResponse{})
			}
			if tt.wantStore {
				require.PanicsWithError(t, nilStorePanic, record)
			} else {
				require.NotPanics(t, record)
			}
		})
	}
}
