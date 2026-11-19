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
	"crypto/sha1"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/repo"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/grpc/client"
	"github.com/google/abfs/shared/remote/grpc/server"
	"github.com/google/abfs/shared/remote/servers/filebased"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

const (
	manifestServer  = "gerrit.example.com"
	manifestProject = "platform/manifest"
)

// pinnedCommit is the commit that testManifest pins by hash; it is on the server only when a test puts it there.
var pinnedCommit = commitContent("pinned")

const testManifest = `<manifest>
  <remote name="aosp" fetch=".." />
  <default revision="main" remote="aosp" />
  <project name="platform/a" path="a" />
  <project name="platform/tools" path="tools" groups="notdefault,tools" />
  <project name="platform/c" path="c" revision="%s" />
</manifest>`

func commitContent(msg string) []byte {
	return fmt.Appendf(nil, "tree %s\nauthor A <a@example.com> 1 +0000\ncommitter A <a@example.com> 1 +0000\n\n%s\n", entities.EmptyDirHash, msg)
}

func encodeObject(objType string, content []byte) (entities.Hash, []byte) {
	var buf bytes.Buffer
	_ = entities.WriteObject(objType, content, &buf)
	return entities.Hash(sha1.Sum(buf.Bytes())), buf.Bytes()
}

func putObject(t *testing.T, s remote.RemoteProvider, objType string, content []byte) entities.Hash {
	key, encoded := encodeObject(objType, content)
	require.NoError(t, s.PutObject(context.Background(), key, remote.NewReaderFromBytes(encoded)))
	return key
}

func putRef(t *testing.T, s remote.RemoteProvider, server, project, ref string, h entities.Hash) {
	require.NoError(t, s.PutRefs(context.Background(), server, project, []entities.RefHash{{Ref: ref, Hash: h}}))
}

// putManifestCommit writes the files as the tree of a manifest project commit and returns the commit.
func putManifestCommit(t *testing.T, s remote.RemoteProvider, files map[string]string) entities.Hash {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var tree bytes.Buffer
	for _, name := range names {
		blob := putObject(t, s, "blob", []byte(files[name]))
		tree.WriteString("100644 " + name + "\x00")
		tree.Write(blob[:])
	}
	treeHash := putObject(t, s, "tree", tree.Bytes())
	return putObject(t, s, "commit", fmt.Appendf(nil, "tree %s\nauthor A <a@example.com> 1 +0000\ncommitter A <a@example.com> 1 +0000\n\nmanifest\n", treeHash))
}

// newServer holds a manifest branch "main" with testManifest as default.xml, and nothing else.
func newServer(t *testing.T) *inmemory.InMemoryServer {
	s := inmemory.New()
	t.Cleanup(func() {
		_ = s.Close()
	})
	pinned, _ := encodeObject("commit", pinnedCommit)
	commit := putManifestCommit(t, s, map[string]string{"default.xml": fmt.Sprintf(testManifest, pinned)})
	putRef(t, s, manifestServer, manifestProject, "refs/heads/main", commit)
	return s
}

func testOpts(branch string, groups ...string) opts {
	return opts{
		manifestServer:      manifestServer,
		manifestProjectName: manifestProject,
		manifestScheme:      "https",
		branch:              branch,
		branchFile:          "default.xml",
		groups:              groups,
	}
}

func TestCheck_Ready(t *testing.T) {
	require := require.New(t)
	s := newServer(t)
	a := putObject(t, s, "commit", commitContent("a"))
	putRef(t, s, manifestServer, "platform/a", "refs/heads/main", a)
	c := putObject(t, s, "commit", pinnedCommit)

	r, err := check(context.Background(), s, testOpts("main"))
	require.NoError(err)
	require.True(r.Ready)
	require.Equal("refs/heads/main", r.ManifestRef)
	require.Equal(2, r.Total)
	require.Equal(2, r.Resolved)
	require.Empty(r.Unresolved)
	require.Equal(a.String(), r.pinned.Project[0].Revision)
	require.Equal(c.String(), r.pinned.Project[2].Revision)
}

func TestCheck_ListsEveryMissingProject(t *testing.T) {
	require := require.New(t)
	s := newServer(t)

	r, err := check(context.Background(), s, testOpts("main"))
	require.NoError(err)
	require.False(r.Ready)
	require.Equal(2, r.Total)
	require.Equal(0, r.Resolved)
	require.Equal([]unresolved{
		{Project: "platform/a", Path: "a", Revision: "main", URL: "https://gerrit.example.com/", Reason: reasonRevisionNotFound},
		{Project: "platform/c", Path: "c", Revision: r.pinned.Project[2].Revision, Reason: reasonCommitNotFound},
	}, r.Unresolved)
}

func TestCheck_Groups(t *testing.T) {
	require := require.New(t)
	s := newServer(t)
	a := putObject(t, s, "commit", commitContent("a"))
	putRef(t, s, manifestServer, "platform/a", "refs/heads/main", a)
	putObject(t, s, "commit", pinnedCommit)

	r, err := check(context.Background(), s, testOpts("main", "default", "tools"))
	require.NoError(err)
	require.False(r.Ready)
	require.Equal(3, r.Total)
	require.Len(r.Unresolved, 1)
	require.Equal("platform/tools", r.Unresolved[0].Project)
}

func TestCheck_NoProjectSelected(t *testing.T) {
	require := require.New(t)
	s := newServer(t)

	r, err := check(context.Background(), s, testOpts("main", "nosuchgroup"))
	require.NoError(err)
	require.False(r.Ready)
	require.Equal([]unresolved{{Groups: []string{"nosuchgroup"}, Reason: reasonNoProjects}}, r.Unresolved)
}

func TestCheck_IncludeAndSecondHost(t *testing.T) {
	require := require.New(t)
	s := newServer(t)
	commit := putManifestCommit(t, s, map[string]string{
		"default.xml": `<manifest>
  <remote name="aosp" fetch=".." />
  <default revision="main" remote="aosp" />
  <project name="platform/a" path="a" />
  <include name="vendor.xml" />
</manifest>`,
		"vendor.xml": `<manifest>
  <remote name="vendor" fetch="https://vendor.example.com" revision="release" />
  <project name="vendor/v" path="vendor/v" remote="vendor" />
</manifest>`,
	})
	putRef(t, s, manifestServer, manifestProject, "refs/heads/split", commit)
	a := putObject(t, s, "commit", commitContent("a"))
	putRef(t, s, manifestServer, "platform/a", "refs/heads/main", a)

	r, err := check(context.Background(), s, testOpts("split"))
	require.NoError(err)
	require.False(r.Ready)
	require.Equal([]unresolved{{Project: "vendor/v", Path: "vendor/v", Revision: "release", URL: "https://vendor.example.com", Reason: reasonRevisionNotFound}}, r.Unresolved)

	v := putObject(t, s, "commit", commitContent("v"))
	putRef(t, s, "vendor.example.com", "vendor/v", "refs/heads/release", v)
	r, err = check(context.Background(), s, testOpts("split"))
	require.NoError(err)
	require.True(r.Ready)
	require.Equal(2, r.Resolved)
}

func TestCheck_ManifestBranchMissing(t *testing.T) {
	require := require.New(t)
	s := newServer(t)

	r, err := check(context.Background(), s, testOpts("android-15"))
	require.NoError(err)
	require.False(r.Ready)
	require.Empty(r.ManifestCommit)
	require.Equal([]unresolved{{Revision: "android-15", URL: "https://gerrit.example.com/platform/manifest", Reason: reasonManifestNotFound}}, r.Unresolved)
}

// The classic mount needs the manifest branch under refs/heads/, so a tag must not read as ready.
func TestCheck_ManifestTagIsNotABranch(t *testing.T) {
	require := require.New(t)
	s := newServer(t)
	hashes, err := s.GetRefs(context.Background(), manifestServer, manifestProject, []string{"refs/heads/main"})
	require.NoError(err)
	putRef(t, s, manifestServer, manifestProject, "refs/tags/v1", hashes[0])

	r, err := check(context.Background(), s, testOpts("v1"))
	require.NoError(err)
	require.False(r.Ready)
	require.Equal(reasonManifestIsTag, r.Unresolved[0].Reason)
}

func TestCheck_PinnedManifestMountsTheSameCommits(t *testing.T) {
	require := require.New(t)
	s := newServer(t)
	a := putObject(t, s, "commit", commitContent("a"))
	putRef(t, s, manifestServer, "platform/a", "refs/heads/main", a)
	putObject(t, s, "commit", pinnedCommit)
	r, err := check(context.Background(), s, testOpts("main"))
	require.NoError(err)
	path := filepath.Join(t.TempDir(), "pinned.xml")
	require.NoError(writePinned(path, r.pinned))

	// Move the branch on: a mount of the pinned file must keep the checked commit.
	a2 := putObject(t, s, "commit", commitContent("a2"))
	putRef(t, s, manifestServer, "platform/a", "refs/heads/main", a2)

	store := remote.NewRemoteStore(s)
	rp := repo.New(store, casgit.New(store), manifestServer, manifestProject, "https", "", false)
	require.NoError(rp.ManifestFromFile(path))
	for i, p := range rp.PinnedManifest().Project {
		require.Equal(r.pinned.Project[i].Revision, p.Revision, p.Name)
	}
}

// fileBasedServer serves an empty file-based server over gRPC, as abfs server file-based does.
func fileBasedServer(t *testing.T) *client.ABFSClient {
	require := require.New(t)
	fserver, err := filebased.New(t.TempDir())
	require.NoError(err)
	s, err := server.NewGRPCServer(&server.GRPCServerOpts{Server: fserver})
	require.NoError(err)
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(err)
	go func() {
		if err := s.Serve(lis); err != nil {
			t.Errorf("Failed to serve the ABFS Server: %v", err)
		}
	}()
	c, err := client.NewABFSClient(&client.ABFSClientOpts{Servers: []string{lis.Addr().String()}, AuthInfo: instance.CreateAuthInfo(instance.AuthTypeNone, "", "", true)})
	require.NoError(err)
	t.Cleanup(func() {
		_ = c.Close()
		s.GracefulStop()
		_ = fserver.Close()
	})
	return c
}

func TestCheck_EmptyServerOverGRPC(t *testing.T) {
	require := require.New(t)

	r, err := check(context.Background(), fileBasedServer(t), testOpts("main"))
	require.NoError(err)
	require.False(r.Ready)
	require.Equal(reasonManifestNotFound, r.Unresolved[0].Reason)
}

func TestRun_WaitsUntilReady(t *testing.T) {
	require := require.New(t)
	c := &readyCmd{wait: true, timeout: time.Minute, interval: time.Millisecond}
	calls := 0
	err := c.run(context.Background(), func(ctx context.Context) (*report, error) {
		calls++
		switch calls {
		case 1:
			return &report{Total: 1}, nil
		case 2:
			return nil, errors.New("connection reset")
		}
		return &report{Ready: true, Total: 1, Resolved: 1}, nil
	})
	require.NoError(err)
	require.Equal(3, calls)
}

func requireExitCode(t *testing.T, want int, err error) {
	var cliErr *clierr.CLIError
	require.ErrorAs(t, err, &cliErr)
	require.Equal(t, want, cliErr.ExitCode())
}

func TestRun_ExitCodes(t *testing.T) {
	tests := []struct {
		name string
		r    *report
		err  error
		want int
	}{
		{name: "ready", r: &report{Ready: true}, want: 0},
		{name: "not ready", r: &report{Unresolved: []unresolved{{Project: "p", Reason: reasonRevisionNotFound}}}, want: 1},
		{name: "error", err: errors.New("unreachable"), want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &readyCmd{timeout: time.Minute}
			err := c.run(context.Background(), func(ctx context.Context) (*report, error) {
				return tt.r, tt.err
			})
			if tt.want == 0 {
				require.NoError(t, err)
				return
			}
			requireExitCode(t, tt.want, err)
		})
	}
}

func TestRun_WaitTimesOut(t *testing.T) {
	c := &readyCmd{wait: true, timeout: 20 * time.Millisecond, interval: time.Millisecond}
	err := c.run(context.Background(), func(ctx context.Context) (*report, error) {
		return &report{Total: 1}, nil
	})
	requireExitCode(t, 1, err)
}

// A deadline inside a round must read as not ready, like one between rounds, with or without --wait.
func TestRun_TimeoutBoundsACheck(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(fmt.Sprintf("wait=%v", wait), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pinned.xml")
			require.NoError(t, os.WriteFile(path, []byte("<manifest/>"), 0o644))
			c := &readyCmd{wait: wait, output: path, timeout: 20 * time.Millisecond, interval: time.Millisecond}
			err := c.run(context.Background(), func(ctx context.Context) (*report, error) {
				<-ctx.Done()
				return nil, fmt.Errorf("cannot load manifest commit: %w", ctx.Err())
			})
			requireExitCode(t, 1, err)
			require.NoFileExists(t, path)
		})
	}
}

func TestRun_ErrorBeforeTheDeadlineIsExit2(t *testing.T) {
	c := &readyCmd{wait: true, timeout: 20 * time.Millisecond, interval: time.Millisecond}
	err := c.run(context.Background(), func(ctx context.Context) (*report, error) {
		return nil, errors.New("permission denied")
	})
	requireExitCode(t, 2, err)
}

func TestRun_CancelIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &readyCmd{wait: true, timeout: time.Minute, interval: time.Minute}
	err := c.run(ctx, func(ctx context.Context) (*report, error) {
		cancel()
		return &report{Total: 1}, nil
	})
	requireExitCode(t, 2, err)
}

func TestRun_OutputRemovedWhenNotReady(t *testing.T) {
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "pinned.xml")
	require.NoError(os.WriteFile(path, []byte("<manifest/>"), 0o644))
	c := &readyCmd{output: path, timeout: time.Minute}
	err := c.run(context.Background(), func(ctx context.Context) (*report, error) {
		return &report{Total: 1, Unresolved: []unresolved{{Project: "p", Reason: reasonRevisionNotFound}}}, nil
	})
	requireExitCode(t, 1, err)
	require.NoFileExists(path)
}

// stallingServer answers tree reads only when their context ends, as a server that stopped replying.
// Trees are read through the CAS view of RemoteStore, which passes no context of its own.
type stallingServer struct {
	*inmemory.InMemoryServer
}

func (s *stallingServer) GetObject(ctx context.Context, key entities.Hash) (remote.ContentReadCloser, error) {
	headers, err := s.Headers(ctx, []entities.Hash{key})
	if err != nil || headers[0].Type != "tree" {
		return s.InMemoryServer.GetObject(ctx, key)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCheck_DeadlineReachesTreeReads(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := check(ctx, &stallingServer{newServer(t)}, testOpts("main"))
	require.ErrorIs(err, context.DeadlineExceeded)
}

func TestRun_OutputWrittenWhenReady(t *testing.T) {
	require := require.New(t)
	s := newServer(t)
	a := putObject(t, s, "commit", commitContent("a"))
	putRef(t, s, manifestServer, "platform/a", "refs/heads/main", a)
	putObject(t, s, "commit", pinnedCommit)
	dir := t.TempDir()
	path := filepath.Join(dir, "pinned.xml")
	c := &readyCmd{output: path, timeout: time.Minute}

	require.NoError(c.run(context.Background(), func(ctx context.Context) (*report, error) {
		return check(ctx, s, testOpts("main"))
	}))
	content, err := os.ReadFile(path)
	require.NoError(err)
	require.Contains(string(content), a.String())
	entries, err := os.ReadDir(dir)
	require.NoError(err)
	require.Len(entries, 1, "no temporary file left behind")
}

func TestRun_OutputNotAFile(t *testing.T) {
	c := &readyCmd{output: t.TempDir(), timeout: time.Minute}
	err := c.run(context.Background(), func(ctx context.Context) (*report, error) {
		return &report{Total: 1, Unresolved: []unresolved{{Project: "p", Reason: reasonRevisionNotFound}}}, nil
	})
	requireExitCode(t, 2, err)
}
