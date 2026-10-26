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

package resolve

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/grpc/client"
	"github.com/google/abfs/shared/remote/grpc/server"
	"github.com/google/abfs/shared/remote/servers"
	"github.com/google/abfs/shared/remote/servers/filebased"
)

// refTable answers SearchRefs as the stores do: the exact ref, else every ref below it.
type refTable struct {
	refs     map[string]map[string]map[string]entities.Hash
	searches []string
}

func (s *refTable) SearchRefs(server, project, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	s.searches = append(s.searches, server+" "+refPrefix)
	projects, ok := s.refs[server]
	if !ok {
		return nil, os.ErrNotExist
	}
	var results []entities.ProjectRefHash
	for name, refs := range projects {
		if project != "" && project != name {
			continue
		}
		if h, ok := refs[refPrefix]; ok {
			results = append(results, entities.ProjectRefHash{Project: name, Ref: refPrefix, Hash: h})
			continue
		}
		for ref, h := range refs {
			if strings.HasPrefix(ref, refPrefix+"/") {
				results = append(results, entities.ProjectRefHash{Project: name, Ref: ref, Hash: h})
			}
		}
	}
	return results, nil
}

func TestCandidates(t *testing.T) {
	tests := []struct {
		revision string
		want     []string
	}{
		{revision: "main", want: []string{"refs/heads/main", "refs/tags/main"}},
		{revision: "android-15.0.0_r36", want: []string{"refs/heads/android-15.0.0_r36", "refs/tags/android-15.0.0_r36"}},
		{revision: "refs/heads/main", want: []string{"refs/heads/main"}},
		{revision: "refs/tags/v1", want: []string{"refs/tags/v1"}},
	}
	for _, tt := range tests {
		t.Run(tt.revision, func(t *testing.T) {
			require := require.New(t)
			require.Equal(tt.want, Candidates(tt.revision))
		})
	}
}

func TestFetchURL(t *testing.T) {
	const manifestURL = "https://gerrit.example.com/platform/manifest"
	tests := []struct {
		fetch string
		want  string
	}{
		{fetch: "..", want: "https://gerrit.example.com/"},
		{fetch: "../aosp", want: "https://gerrit.example.com/aosp"},
		{fetch: "../", want: "https://gerrit.example.com/"},
		{fetch: ".", want: "https://gerrit.example.com/platform/"},
		{fetch: "./mirror/", want: "https://gerrit.example.com/platform/mirror/"},
		{fetch: "//other.example.com/", want: "https://other.example.com/"},
		{fetch: "https://android.googlesource.com/", want: "https://android.googlesource.com/"},
		{fetch: "https://gerrit.example.com/a/", want: "https://gerrit.example.com/a/"},
		{fetch: "ssh://user@gerrit.example.com:29418/", want: "ssh://user@gerrit.example.com:29418/"},
	}
	for _, tt := range tests {
		t.Run(tt.fetch, func(t *testing.T) {
			require := require.New(t)
			got, err := FetchURL(tt.fetch, manifestURL)
			require.NoError(err)
			require.Equal(tt.want, got)
		})
	}
}

func TestFetchURL_NestedManifestProject(t *testing.T) {
	require := require.New(t)
	got, err := FetchURL("../../aosp", "https://android.googlesource.com/platform/manifests/release")
	require.NoError(err)
	require.Equal("https://android.googlesource.com/aosp", got)
}

func TestRevisions(t *testing.T) {
	require := require.New(t)

	branchHash := entities.NewHash("1111111111111111111111111111111111111111")
	tagHash := entities.NewHash("2222222222222222222222222222222222222222")
	bothBranchHash := entities.NewHash("3333333333333333333333333333333333333333")
	pinnedHash := entities.NewHash("5555555555555555555555555555555555555555")
	otherHash := entities.NewHash("6666666666666666666666666666666666666666")

	rt := &refTable{refs: map[string]map[string]map[string]entities.Hash{
		"gerrit.example.com": {
			"platform/foo": {
				"refs/heads/main":  branchHash,
				"refs/tags/v1":     tagHash,
				"refs/heads/both":  bothBranchHash,
				"refs/tags/both":   tagHash,
				"refs/heads/dev/x": otherHash,
			},
			"platform/bar": {
				"refs/heads/main": otherHash,
			},
		},
	}}

	const fetch = "https://gerrit.example.com/a/"
	reqs := []Request{
		{Project: "platform/foo", FetchURL: fetch, Revision: "main"},
		{Project: "platform/foo", FetchURL: fetch, Revision: "refs/heads/main"},
		{Project: "platform/foo", FetchURL: fetch, Revision: "v1"},
		{Project: "platform/foo", FetchURL: fetch, Revision: "refs/tags/v1"},
		{Project: "platform/foo", FetchURL: fetch, Revision: "both"},
		{Project: "platform/foo", FetchURL: fetch, Revision: pinnedHash.String()},
		{Project: "platform/bar", FetchURL: fetch, Revision: "main"},
	}
	results, err := Revisions(rt, reqs)
	require.NoError(err)
	require.Equal([]Result{
		{Ref: "refs/heads/main", Hash: branchHash},
		{Ref: "refs/heads/main", Hash: branchHash},
		{Ref: "refs/tags/v1", Hash: tagHash},
		{Ref: "refs/tags/v1", Hash: tagHash},
		{Ref: "refs/heads/both", Hash: bothBranchHash},
		{Hash: pinnedHash},
		{Ref: "refs/heads/main", Hash: otherHash},
	}, results)

	// One search per candidate ref and host, shared by every project on the host.
	require.Len(rt.searches, 6)
}

func TestRevisions_NamesEveryUnresolvedRequest(t *testing.T) {
	require := require.New(t)

	rt := &refTable{refs: map[string]map[string]map[string]entities.Hash{
		"gerrit.example.com": {
			"platform/present": {"refs/heads/main": entities.NewHash("1111111111111111111111111111111111111111")},
			"platform/prefix":  {"refs/heads/main/sub": entities.NewHash("2222222222222222222222222222222222222222")},
		},
	}}

	reqs := []Request{
		{Project: "platform/missing", FetchURL: "https://gerrit.example.com/", Revision: "main"},
		{Project: "platform/present", FetchURL: "https://gerrit.example.com/", Revision: "main"},
		{Project: "platform/prefix", FetchURL: "https://gerrit.example.com/", Revision: "main"},
		{Project: "platform/elsewhere", FetchURL: "https://unknown.example.com/", Revision: "main"},
	}
	results, err := Revisions(rt, reqs)
	require.Len(results, 4)
	require.False(results[1].Hash.IsZero())

	var unresolved *UnresolvedError
	require.ErrorAs(err, &unresolved)
	require.Equal([]Request{reqs[0], reqs[2], reqs[3]}, unresolved.Requests)
	require.ErrorContains(err, `cannot resolve "main" for project "platform/missing" at URL "https://gerrit.example.com/"`)
	require.ErrorContains(err, "platform/prefix")
	require.ErrorContains(err, "platform/elsewhere")
}

func TestMatchGroups(t *testing.T) {
	tests := []struct {
		name    string
		project []string
		filter  []string
		want    bool
	}{
		{name: "no filter selects default", project: []string{"a"}, filter: nil, want: true},
		{name: "no filter skips notdefault", project: []string{"a", "notdefault"}, filter: nil, want: false},
		{name: "named group", project: []string{"a"}, filter: []string{"b", "a"}, want: true},
		{name: "other group", project: []string{"a"}, filter: []string{"b"}, want: false},
		{name: "all", project: []string{"notdefault"}, filter: []string{"all"}, want: true},
		{name: "minus deselects", project: []string{"a"}, filter: []string{"-a", "c"}, want: false},
		{name: "last match wins", project: []string{"a"}, filter: []string{"-a", "a"}, want: true},
		{name: "minus after include", project: []string{"a"}, filter: []string{"a", "-a"}, want: false},
		{name: "empty entries ignored", project: []string{"a"}, filter: []string{"", "a"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			require.Equal(tt.want, MatchGroups(tt.project, tt.filter))
		})
	}
}

// fileBasedStore serves a file-based server over gRPC and returns a client store for it.
func fileBasedStore(t *testing.T) *remote.RemoteStore {
	require := require.New(t)
	fserver, err := filebased.New(t.TempDir(), servers.NoLinkValidationOpt(true))
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
	return remote.NewRemoteStore(c)
}

func TestRevisions_UnknownHostOverGRPC(t *testing.T) {
	require := require.New(t)
	store := fileBasedStore(t)
	main := entities.NewHash("1111111111111111111111111111111111111111")
	require.NoError(store.PutRefs("gerrit.example.com", "platform/foo", []entities.RefHash{{Ref: "refs/heads/main", Hash: main}}))

	reqs := []Request{
		{Project: "platform/foo", FetchURL: "https://gerrit.example.com/platform/foo", Revision: "main"},
		{Project: "vendor/a", FetchURL: "https://vendor.example.com/vendor/a", Revision: "main"},
		{Project: "vendor/b", FetchURL: "https://vendor.example.com/vendor/b", Revision: "refs/tags/v1"},
	}
	results, err := Revisions(store, reqs)
	var unresolved *UnresolvedError
	require.ErrorAs(err, &unresolved)
	require.Equal(reqs[1:], unresolved.Requests)
	require.Equal(main, results[0].Hash)
}
