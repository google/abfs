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

const (
	buildDateFile     = "out/build_date.txt"
	buildHostnameFile = "out/soong/build_hostname.txt"
	buildNumberFile   = "out/soong/build_number.txt"
	buildPropFile     = "out/target/product/test/system/build.prop"
)

var (
	hashA = entities.NewHash("1111111111111111111111111111111111111111")
	hashB = entities.NewHash("2222222222222222222222222222222222222222")
)

func TestIgnoreMatch_BuildMetadata(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		state entities.PatchEntryState
		want  bool
	}{
		{name: "undeclared build date read", path: buildDateFile, state: entities.PatchEntryStateExtraRead, want: true},
		{name: "declared build date", path: buildDateFile, state: entities.PatchEntryStateIn, want: true},
		{name: "declared host name", path: buildHostnameFile, state: entities.PatchEntryStateIn, want: true},
		{name: "undeclared host name read", path: buildHostnameFile, state: entities.PatchEntryStateExtraRead, want: true},
		{name: "order-only build date", path: buildDateFile, state: entities.PatchEntryStateOrderOnly, want: true},
		{name: "undeclared build number read", path: buildNumberFile, state: entities.PatchEntryStateExtraRead},
		{name: "declared source file", path: "src/a.txt", state: entities.PatchEntryStateIn},
	}

	c := NewABFSBackend()
	c.config = &Configuration{OrderOnlyAsRegularInput: true}
	c.matchFiles = newMatchFiles("out")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ent := &entities.PatchEntry{Path: tt.path, Mode: entities.FileModeRegular, Hash: hashA, State: tt.state}
			require.Equal(t, tt.want, c.ignoreMatch(ent))
		})
	}
}

func TestCleanMatch_BuildDateChanged(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "build date", path: buildDateFile},
		{name: "source file", path: "src/a.txt", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewABFSBackend()
			c.config = &Configuration{OrderOnlyAsRegularInput: true}
			c.matchFiles = newMatchFiles("out")
			c.hashes.Put(tt.path, &HashStat{Hash: hashB, Mode: entities.FileModeRegular})

			// The read was recorded with hash A; the file now has hash B.
			err := c.cleanMatch(&entities.PatchEntry{
				Path:  tt.path,
				Mode:  entities.FileModeRegular,
				Hash:  hashA,
				State: entities.PatchEntryStateExtraRead,
			})
			if tt.wantErr {
				require.True(t, binding.IsMatchError(err), "got %v", err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type metadataGraphBackend struct{}

func (metadataGraphBackend) GraphLoading() error                   { return nil }
func (metadataGraphBackend) GraphLoaded(*graph.Graph) error        { return nil }
func (metadataGraphBackend) GetDeps(*graph.Node) ([]string, error) { return nil, nil }

// buildPropGraph loads a graph with one build.prop step that declares the host name
// file and a source file as implicit inputs, as Soong writes the rule, and runs the
// backend's GraphLoaded on it.
func buildPropGraph(t *testing.T, c *ABFSBackend) *graph.Graph {
	t.Helper()
	ctx := context.WithValue(context.Background(), ninjaruntime.RunParamsKey, &ninjaruntime.RunParameters{
		CliFlags: &ninjaruntime.CliFlags{ConcurrencyLevel: 2},
	})
	ctx = context.WithValue(ctx, ninjaruntime.BackendKey, graph.GraphBackend(metadataGraphBackend{}))

	n := &ninja.NinjaFile{
		FileName: "build.ninja",
		BuildDir: "out",
		Rules:    []*ninja.Rule{{Name: "gen_build_prop", Command: "gen_build_prop --out=$out"}},
		Builds: []*ninja.Build{{
			Outputs:        []string{buildPropFile},
			RuleName:       "gen_build_prop",
			ImplicitInputs: []string{buildHostnameFile, "src/a.txt"},
			NinjaFile:      "build.ninja",
		}, {
			// Two outputs put one build under two keys of Targets.
			Outputs:        []string{buildPropFile + ".a", buildPropFile + ".b"},
			RuleName:       "gen_build_prop",
			ImplicitInputs: []string{buildHostnameFile, "src/a.txt"},
			NinjaFile:      "build.ninja",
		}},
	}
	require.NoError(t, ninja.ExpandNinja(ctx, n))
	files := ninja.NewFiles(n.FileName)
	files.AddFile(n.FileName, n)

	g := graph.NewGraph()
	require.NoError(t, g.Load(ctx, files, []string{buildPropFile, buildPropFile + ".a"}, false))
	require.NoError(t, c.GraphLoaded(g))
	return g
}

// newBuildPropBackend returns a backend with the graph from buildPropGraph loaded and
// the given hashes for the host name file and the source file. Hash requests are
// answered from those values, as a casfs mount would report them.
func newBuildPropBackend(t *testing.T, hostname, src entities.Hash) (*ABFSBackend, *build.Step) {
	t.Helper()
	c := NewABFSBackend()
	c.config = &Configuration{OrderOnlyAsRegularInput: true}
	g := buildPropGraph(t, c)
	c.hashes.Put(buildHostnameFile, &HashStat{Hash: hostname, Mode: entities.FileModeRegular, Size: 6})
	c.hashes.Put("src/a.txt", &HashStat{Hash: src, Mode: entities.FileModeRegular, Size: 1})
	go func() {
		for req := range c.hashFetchC {
			req.callback(c.hashes.Get(req.file), nil)
		}
	}()
	t.Cleanup(func() { close(c.hashFetchC) })

	step := build.NewStep(g.Targets.Get(buildPropFile))
	step.LoadCommand()
	return c, step
}

func TestGraphLoaded_HostnameNotInKey(t *testing.T) {
	requestHash := func(t *testing.T, hostname, src entities.Hash) entities.Hash {
		c, step := newBuildPropBackend(t, hostname, src)
		require.Equal(t, []string{"src/a.txt"}, step.Node.Build.ImplicitInputs)
		require.Contains(t, step.Node.Build.OrderOnlyInputs, buildHostnameFile)

		req, err := c.toEvalRequest(step)
		require.NoError(t, err)
		require.NoError(t, req.Encode())
		return req.Hash
	}

	base := requestHash(t, hashA, hashA)
	require.Equal(t, base, requestHash(t, hashB, hashA), "a new host name must not change the key")
	require.NotEqual(t, base, requestHash(t, hashA, hashB), "a changed source file must change the key")
}

func TestGraphLoaded_MultipleOutputsMovedOnce(t *testing.T) {
	c, _ := newBuildPropBackend(t, hashA, hashA)
	b := c.graph.Targets.Get(buildPropFile + ".a").Build
	require.Equal(t, []string{"src/a.txt"}, b.ImplicitInputs)
	require.Equal(t, []string{buildHostnameFile}, b.OrderOnlyInputs)
}

func TestRemoteOrderOnlyPatch_Hostname(t *testing.T) {
	c, step := newBuildPropBackend(t, hashA, hashA)
	hs := c.hashes.Get(buildHostnameFile)

	want := &binding.PatchObject{
		Version: binding.CurrentPatchVersion,
		Patch: entities.Patch{{
			Path: buildHostnameFile,
			Hash: hs.Hash,
			Mode: hs.Mode,
			Size: hs.Size,
		}},
	}
	require.NoError(t, want.Encode())
	// The backend stores a new patch object; mark this one stored so no store is needed.
	c.remotePatches.Store(want.Hash, struct{}{})

	h, err := c.remoteOrderOnlyPatch(context.Background(), &Eval{step: step})
	require.NoError(t, err)
	require.Equal(t, want.Hash, h)
}
