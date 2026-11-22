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
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/ninja-go/build"
	"github.com/google/ninja-go/graph"
	"github.com/google/ninja-go/ninja"
	ninjaruntime "github.com/google/ninja-go/runtime"
	"github.com/google/ninja-go/xxmap"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago/ninjalog"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/store/localcache"
)

// contentCASFS hashes files from their content, where casfs would return the hash as an
// extended attribute.
type contentCASFS struct {
	bindcas.CASFS
}

func (contentCASFS) HashSize(path string) (entities.Hash, int64, error) {
	if target, err := os.Readlink(path); err == nil {
		return objects.HashBlobBytes([]byte(target)), int64(len(target)), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return entities.Hash{}, 0, err
	}
	return objects.HashBlobBytes(b), int64(len(b)), nil
}

// failingCASFS fails to return any hash.
type failingCASFS struct {
	bindcas.CASFS
}

func (failingCASFS) HashSize(string) (entities.Hash, int64, error) {
	return entities.Hash{}, 0, errors.New("no hash")
}

func setCASFS(t *testing.T, fs bindcas.CASFS) {
	bindcas.SetGlobalCASFS(fs)
	t.Cleanup(func() { bindcas.SetGlobalCASFS(bindcas.NewExternalCASFS("")) })
}

func writeOutFile(t *testing.T, name, content string) {
	require.NoError(t, os.MkdirAll(filepath.Dir(name), 0755))
	require.NoError(t, os.WriteFile(name, []byte(content), 0644))
}

// newFreshOutBackend returns a backend with an empty hash cache, as at the start of a build.
func newFreshOutBackend() *ABFSBackend {
	c := NewABFSBackend()
	c.config = &Configuration{}
	c.outDir = "out"
	c.matchFiles = newMatchFiles(c.outDir)
	c.graph = &graph.Graph{Targets: xxmap.NewStringMap[*graph.Node]()}
	return c
}

func TestReadUnwrittenTargets_EmptyCommand(t *testing.T) {
	setCASFS(t, contentCASFS{bindcas.NewExternalCASFS("")})
	t.Chdir(t.TempDir())

	// The file exists before the step runs, as soong_ui writes it before ninja starts.
	const target = "out/soong/build_number.txt"
	writeOutFile(t, target, "1\n")

	node := &graph.Node{Targets: []string{target}, Build: &ninja.Build{RuleName: "rule1"}}
	eval := &Eval{
		step: &build.Step{Node: node},
		req:  &binding.EvalRequest{Targets: node.Targets, Command: `/bin/bash -c ""`},
	}

	// The tapped run of the empty command reads and writes nothing.
	resp := binding.EvalRespFromTapResult(entities.TapCmdResult{})
	binding.ProcessResp(binding.EvalPostProcess{Resp: resp, Req: eval.req})
	require.Contains(t, missingTargets(resp, node.Targets), target)

	require.NoError(t, readUnwrittenTargets(resp, node.Targets))
	require.Empty(t, missingTargets(resp, node.Targets))
	require.Equal(t, entities.PatchEntryStateMissingOut, resp.MissingWrites[target])

	require.NoError(t, resp.Encode())
	stored, err := binding.DecodeEvalResponse(resp.Hash, resp.Encoded)
	require.NoError(t, err)
	require.Empty(t, missingTargets(stored, node.Targets))

	inputOf := func(c *ABFSBackend) entities.PatchEntry {
		var inputs []entities.PatchEntry
		require.NoError(t, c.addInput(target, false, &inputs, map[string]struct{}{}))
		require.Len(t, inputs, 1)
		return inputs[0]
	}

	// A later build in a fresh out dir with the same content hits.
	c := newFreshOutBackend()
	_, err = c.updateStat(target)
	require.NoError(t, err)
	_, err = c.reduceMatches(stored, eval)
	require.NoError(t, err)
	before := inputOf(c)

	// New content misses, and a step that reads the target gets another request.
	writeOutFile(t, target, "2\n")
	c = newFreshOutBackend()
	_, err = c.updateStat(target)
	require.NoError(t, err)
	_, err = c.reduceMatches(stored, eval)
	require.True(t, binding.IsMatchError(err), "got %v", err)
	require.NotEqual(t, before.Hash, inputOf(c).Hash)
}

func TestReadUnwrittenTargets_Unchanged(t *testing.T) {
	setCASFS(t, contentCASFS{bindcas.NewExternalCASFS("")})
	t.Chdir(t.TempDir())

	writeOutFile(t, "out/a.txt", "a")
	writeOutFile(t, "out/b.txt", "b")
	require.NoError(t, os.MkdirAll("out/dir", 0755))

	tests := []struct {
		name    string
		resp    *binding.EvalResponse
		targets []string
	}{
		{"failed step", &binding.EvalResponse{ExitCode: 1}, []string{"out/a.txt"}},
		{"wrote another target", &binding.EvalResponse{WritePatch: entities.Patch{{Path: "out/b.txt"}}}, []string{"out/a.txt", "out/b.txt"}},
		{"deleted a file", &binding.EvalResponse{Deletes: entities.DeletePatch{"out/old.txt"}}, []string{"out/a.txt"}},
		{"missing target", &binding.EvalResponse{}, []string{"out/missing.txt"}},
		{"directory target", &binding.EvalResponse{}, []string{"out/dir"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, readUnwrittenTargets(tt.resp, tt.targets))
			require.Empty(t, tt.resp.ReadPatch)
		})
	}
}

func TestReadUnwrittenTargets_Symlink(t *testing.T) {
	setCASFS(t, contentCASFS{bindcas.NewExternalCASFS("")})
	t.Chdir(t.TempDir())

	writeOutFile(t, "out/a.txt", "a")
	require.NoError(t, os.Symlink("a.txt", "out/link"))

	resp := &binding.EvalResponse{}
	require.NoError(t, readUnwrittenTargets(resp, []string{"out/link"}))
	require.Equal(t, entities.Patch{{
		Path:  "out/link",
		Mode:  entities.FileModeSymlink,
		Hash:  objects.HashBlobBytes([]byte("a.txt")),
		Size:  int64(len("a.txt")),
		State: entities.PatchEntryStateExtraRead,
	}}, resp.ReadPatch)
}

// fakeTap runs the command after "--" and reports only its exit code, as a tap that saw no reads
// or writes.
const fakeTap = `#!/bin/sh
while [ "$1" != "--" ]; do shift; done
shift
"$@" >/dev/null 2>&1
printf '{"ExitCode":%d}\n' $?
`

// warnLogger records the warnings of a backend.
type warnLogger struct {
	ninjaruntime.Logger
	warnings []string
}

func (l *warnLogger) Warnf(format string, a ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, a...))
}

// newTapBackend returns a backend in the current directory that taps with fakeTap and stores
// in a local cache.
func newTapBackend(t *testing.T) (*ABFSBackend, *warnLogger) {
	const tapPath = "prebuilts/build-tools/linux-x86/bin/tap"
	writeOutFile(t, tapPath, fakeTap)
	require.NoError(t, os.Chmod(tapPath, 0755))

	dir := t.TempDir()
	const socket = "abfs.sock"
	l, err := net.Listen("unix", filepath.Join(dir, socket))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	cacheDir := filepath.Join(dir, "cache")
	require.NoError(t, localcache.InitLocalCache(cacheDir))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, localcache.VersionFile), []byte(localcache.CacheVersion), 0644))
	client, err := binding.NewClient(dir, socket, cacheDir, true, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	c := newFreshOutBackend()
	c.abfs = client
	log := &warnLogger{}
	c.log = log
	return c, log
}

func newCommandEval(t *testing.T, target, command string) *Eval {
	b := &ninja.Build{
		Outputs:   []string{target},
		RuleName:  "rule1",
		Rule:      &ninja.Rule{Name: "rule1"},
		KeyValues: []*ninja.KeyValue{{Key: "command", Value: command}},
	}
	b.Scope = new(ninja.NinjaScope).NewBuildScope(b)
	step := build.NewStep(&graph.Node{Targets: b.Outputs, Build: b})
	step.LoadCommand()
	req := &binding.EvalRequest{Targets: step.Node.Targets, Command: step.Command()}
	require.NoError(t, req.Encode())
	return &Eval{step: step, req: req, triedResps: make(map[entities.Hash]struct{})}
}

func TestExecTapLocal_UnwrittenTarget(t *testing.T) {
	setCASFS(t, contentCASFS{bindcas.NewExternalCASFS("")})
	t.Chdir(t.TempDir())
	c, log := newTapBackend(t)

	const target = "out/soong/build_number.txt"
	writeOutFile(t, target, "1\n")
	eval := newCommandEval(t, target, "true")

	resp, err := c.execTapLocal(context.Background(), eval)
	require.NoError(t, err)
	require.Empty(t, log.warnings)
	require.Empty(t, missingTargets(resp, eval.step.Node.Targets))

	stored, err := binding.FetchObject[binding.EvalResponse](context.Background(), c.abfs.Store(), resp.Hash)
	require.NoError(t, err)
	require.Empty(t, missingTargets(stored, eval.step.Node.Targets))

	// The step stays clean while the target has the recorded content and turns dirty after.
	c.logEntries.Put(eval.step.Node, &ninjalog.LogEntry{ReqHash: eval.req.Hash, RespHash: resp.Hash})
	clean, err := c.IsClean(eval.step)
	require.NoError(t, err)
	require.True(t, clean)

	writeOutFile(t, target, "2\n")
	c.hashes = xxmap.NewStringMap[*HashStat]()
	clean, err = c.IsClean(eval.step)
	require.NoError(t, err)
	require.False(t, clean)
}

func TestExecTapLocal_FailedStep(t *testing.T) {
	setCASFS(t, contentCASFS{bindcas.NewExternalCASFS("")})
	t.Chdir(t.TempDir())
	c, _ := newTapBackend(t)

	const target = "out/a.txt"
	writeOutFile(t, target, "a")
	resp, err := c.execTapLocal(context.Background(), newCommandEval(t, target, "false"))
	require.NoError(t, err)
	require.Equal(t, 1, resp.ExitCode)
	require.Empty(t, resp.ReadPatch)
}

func TestExecTapLocal_HashError(t *testing.T) {
	setCASFS(t, failingCASFS{bindcas.NewExternalCASFS("")})
	t.Chdir(t.TempDir())
	c, log := newTapBackend(t)

	const target = "out/a.txt"
	writeOutFile(t, target, "a")
	resp, err := c.execTapLocal(context.Background(), newCommandEval(t, target, "true"))
	require.NoError(t, err)
	require.Equal(t, 0, resp.ExitCode)
	require.Empty(t, resp.ReadPatch)
	require.Len(t, log.warnings, 1)
}
