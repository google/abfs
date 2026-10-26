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

package ready

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

const poolConfig = `
pushers-count: 1
pushers:
- name: pusher-0
repos:
- manifest:
    url: https://gerrit.example.com/platform/manifest
    files:
    - branch: main
      file: default.xml
`

// readyButA holds testManifest with everything on the server except project platform/a.
func readyButA(t *testing.T) *inmemory.InMemoryServer {
	s := newServer(t)
	putObject(t, s, "commit", pinnedCommit)
	return s
}

func withPool(t *testing.T, s remote.RemoteProvider, branch, config string) opts {
	o := testOpts(branch)
	o.pusherConfig = &hashpath.HashPath{Hash: putObject(t, s, "blob", []byte(config))}
	return o
}

func putStatus(t *testing.T, s remote.RemoteProvider, project string, status *binding.ProjectStatus) {
	status.Version = binding.CurrentProjectStatusVersion
	require.NoError(t, status.Encode())
	require.NoError(t, s.PutObject(context.Background(), status.Hash, remote.ContentReaderFromBytes(status.Encoded)))
	putRef(t, s, manifestServer, project, binding.ProjectStatusRef, status.Hash)
}

func reasonOfA(t *testing.T, s remote.RemoteCloser, o opts) unresolved {
	r, err := check(context.Background(), s, o)
	require.NoError(t, err)
	require.Len(t, r.Unresolved, 1)
	require.Equal(t, "platform/a", r.Unresolved[0].Project)
	return r.Unresolved[0]
}

func TestExplain_NotPushedYet(t *testing.T) {
	s := readyButA(t)
	u := reasonOfA(t, s, withPool(t, s, "main", poolConfig))
	require.Equal(t, reasonNotPushed, u.Reason)
	require.Equal(t, "no completed push", u.Detail)
}

func TestExplain_PushStatus(t *testing.T) {
	require := require.New(t)
	s := readyButA(t)
	o := withPool(t, s, "main", poolConfig)

	pushed := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	putStatus(t, s, "platform/a", &binding.ProjectStatus{LastSuccess: pushed})
	u := reasonOfA(t, s, o)
	require.Equal(reasonNotPushed, u.Reason)
	require.Equal("last reported push 2026-10-26T08:00:00Z", u.Detail)

	putStatus(t, s, "platform/a", &binding.ProjectStatus{LastSuccess: pushed, LastError: "fetch failed: 403"})
	u = reasonOfA(t, s, o)
	require.Equal(reasonPushFailed, u.Reason)
	require.Equal("fetch failed: 403", u.Detail)
}

func TestExplain_Config(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		want        string
		wantBlocked bool
	}{
		{name: "no repos", config: `
pushers-count: 1`, want: reasonNotInPool, wantBlocked: true},
		{name: "manifest not in pool, another repo is", config: `
repos:
- manifest:
    url: https://other.example.com/platform/manifest`, want: reasonNotInPool},
		{name: "branch not in pool", config: `
repos:
- manifest:
    url: https://gerrit.example.com/platform/manifest
    files:
    - branch: release
      file: default.xml`, want: reasonBranchNotInPool},
		{name: "excluded", config: poolConfig + `
  overrides:
  - name: platform/a
    exclude: true`, want: reasonExcluded, wantBlocked: true},
		{name: "excluded by regex", config: poolConfig + `
  overrides:
  - name: platform/.*
    regex: true
    exclude: true`, want: reasonExcluded, wantBlocked: true},
		{name: "excluded, but another repo may list it", config: poolConfig + `
  overrides:
  - name: platform/a
    exclude: true
- manifest:
    url: https://other.example.com/platform/manifest`, want: reasonExcluded},
		{name: "invalid fetch-ref-spec", config: poolConfig + `
  project-defaults:
    fetch-ref-spec: ["+refs/heads/*:refs/heads/main"]`, want: reasonInvalidRefSpec, wantBlocked: true},
		// The bare revision main could also be a tag that git follows into the fetched history.
		{name: "bare revision outside fetch-ref-spec", config: poolConfig + `
  overrides:
  - name: platform/a
    fetch-ref-spec: ["+refs/heads/release:refs/heads/release"]`, want: reasonOutsideRefSpec},
		{name: "standalone project entry", config: poolConfig + `
projects:
- url: https://gerrit.example.com/platform/a
  fetch-ref-spec: ["+refs/heads/main:refs/heads/main"]`, want: reasonNotPushed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			s := readyButA(t)
			r, err := check(context.Background(), s, withPool(t, s, "main", tt.config))
			require.NoError(err)
			require.Empty(r.PusherConfigError)
			require.Equal(tt.want, r.Unresolved[0].Reason)
			require.Equal(tt.wantBlocked, r.Unresolved[0].Blocked)
			require.Equal(tt.wantBlocked, r.Blocked)
		})
	}
}

func TestExplain_FullRefOutsideSpecIsBlocked(t *testing.T) {
	require := require.New(t)
	s := newServer(t)
	commit := putManifestCommit(t, s, map[string]string{"default.xml": `<manifest>
  <remote name="aosp" fetch=".." />
  <default revision="refs/heads/main" remote="aosp" />
  <project name="platform/a" path="a" />
</manifest>`})
	putRef(t, s, manifestServer, manifestProject, "refs/heads/full", commit)

	r, err := check(context.Background(), s, withPool(t, s, "full", `
repos:
- manifest:
    url: https://gerrit.example.com/platform/manifest
    files:
    - branch: full
      file: default.xml
  project-defaults:
    fetch-ref-spec: ["+refs/heads/release:refs/heads/release"]`))
	require.NoError(err)
	require.Equal(reasonOutsideRefSpec, r.Unresolved[0].Reason)
	require.True(r.Blocked)
}

func TestExplain_ManifestBranch(t *testing.T) {
	require := require.New(t)
	s := newServer(t)

	r, err := check(context.Background(), s, withPool(t, s, "android-15", poolConfig))
	require.NoError(err)
	require.Equal(reasonNotPushed, r.Unresolved[0].Reason)

	// A narrowed fetch-ref-spec narrows the manifest project too.
	r, err = check(context.Background(), s, withPool(t, s, "android-15", poolConfig+`
  project-defaults:
    fetch-ref-spec: ["+refs/heads/main:refs/heads/main"]`))
	require.NoError(err)
	require.Equal(reasonOutsideRefSpec, r.Unresolved[0].Reason)
}

func TestExplain_UnreadableConfigKeepsReasons(t *testing.T) {
	s := readyButA(t)
	o := testOpts("main")
	o.pusherConfig = &hashpath.HashPath{Hash: entities.NewHash("1111111111111111111111111111111111111111")}
	r, err := check(context.Background(), s, o)
	require.NoError(t, err)
	require.Equal(t, reasonRevisionNotFound, r.Unresolved[0].Reason)
	require.NotEmpty(t, r.PusherConfigError)
}

// The default --pusher-config is a path below the config ref of the ABFS meta project, as the git-pusher reads it.
func TestExplain_DefaultConfigPath(t *testing.T) {
	require := require.New(t)
	s := readyButA(t)
	blob := putObject(t, s, "blob", []byte(poolConfig))
	var inner, outer bytes.Buffer
	inner.WriteString("100644 default\x00")
	inner.Write(blob[:])
	dir := putObject(t, s, "tree", inner.Bytes())
	outer.WriteString("40000 git-pusher\x00")
	outer.Write(dir[:])
	putRef(t, s, entities.ABFSMetaServer, entities.ABFSMetaProject, "config", putObject(t, s, "tree", outer.Bytes()))
	hp, err := hashpath.Parse("config:git-pusher/default")
	require.NoError(err)
	o := testOpts("main")
	o.pusherConfig = hp

	u := reasonOfA(t, s, o)
	require.Equal(reasonNotPushed, u.Reason)
}

func TestRun_WaitStopsWhenBlocked(t *testing.T) {
	c := &readyCmd{wait: true, timeout: time.Minute, interval: time.Millisecond}
	calls := 0
	err := c.run(context.Background(), func(ctx context.Context) (*report, error) {
		calls++
		return &report{Total: 1, Blocked: true, Unresolved: []unresolved{{Project: "p", Reason: reasonExcluded, Blocked: true}}}, nil
	})
	requireExitCode(t, 1, err)
	require.Equal(t, 1, calls)
}
