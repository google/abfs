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

package gcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
)

func putTestBlob(t *testing.T, s remote.RemoteProvider, content string) entities.Hash {
	t.Helper()
	key := objects.HashBlobBytes([]byte(content))
	data := fmt.Appendf(nil, "blob %d\x00%s", len(content), content)
	require.NoError(t, s.PutObject(context.Background(), key, remote.NewContentReader(io.NopCloser(bytes.NewReader(data)), remote.EncodingRaw)))
	return key
}

type refsCallResult struct {
	Refs     []remote.ProjectRefHash
	Hashes   []entities.Hash
	Err      bool
	NotExist bool
}

func refsCall(refs []remote.ProjectRefHash, hashes []entities.Hash, err error) refsCallResult {
	// Empty and nil lists are the same on the wire.
	if len(refs) == 0 {
		refs = nil
	}
	return refsCallResult{Refs: refs, Hashes: hashes, Err: err != nil, NotExist: entities.IsNotExist(err)}
}

// refsScenario runs the same ref calls against a server and returns their results.
func refsScenario(t *testing.T, s remote.RemoteProvider) map[string]refsCallResult {
	t.Helper()
	require := require.New(t)
	ctx := context.Background()
	h := putTestBlob(t, s, "refs scenario target")

	require.NoError(s.PutRefs(ctx, "host-a.example", "proj/a", []remote.RefHash{
		{Ref: "refs/heads/aaa", Hash: h},
		{Ref: "refs/heads/zzz", Hash: entities.ZeroHash},
	}))
	require.NoError(s.PutRefs(ctx, "host-b.example", "proj/b", []remote.RefHash{
		{Ref: "refs/heads/aaa", Hash: h},
		{Ref: "refs/tags/v1", Hash: h},
	}))
	require.NoError(s.PutRefs(ctx, "host-c.example", "proj/c", []remote.RefHash{
		{Ref: "refs/heads/zzz", Hash: h},
	}))

	results := make(map[string]refsCallResult)
	search := func(name, server, project, prefix string, limit int) {
		refs, err := s.SearchRefs(ctx, server, project, prefix, limit)
		results[name] = refsCall(refs, nil, err)
	}
	get := func(name, server, project string, refs ...string) {
		hashes, err := s.GetRefs(ctx, server, project, refs)
		results[name] = refsCall(nil, hashes, err)
	}
	search("no project, limit after host filter", "host-b.example", "", "refs/heads", 1)
	search("no project, limit after tombstones", "host-a.example", "", "refs/heads", 1)
	search("no project, no limit", "host-b.example", "", "refs", 0)
	search("project, empty prefix", "host-b.example", "proj/b", "", 0)
	search("project, trailing slash", "host-b.example", "proj/b", "refs/heads/", 0)
	search("project, partial path component", "host-b.example", "proj/b", "refs/hea", 0)
	search("unknown project", "host-b.example", "no/such", "refs/heads", 0)
	search("unknown server", "host-x.example", "", "refs/heads", 0)
	search("unknown server and project", "host-x.example", "no/such", "refs/heads", 0)
	get("get, known", "host-b.example", "proj/b", "refs/heads/aaa", "refs/heads/missing")
	get("get, tombstone", "host-a.example", "proj/a", "refs/heads/zzz")
	get("get, unknown project", "host-b.example", "no/such", "refs/heads/main")
	get("get, unknown server", "host-x.example", "no/such", "refs/heads/main")
	return results
}

func TestXchgRefs_TooManyRefsIsInvalidArgument(t *testing.T) {
	s, _ := createTestServerWithCASDB(t)
	refs := make([]remote.RefXchg, 2001)
	for i := range refs {
		refs[i] = remote.RefXchg{Ref: fmt.Sprintf("refs/heads/b%d", i)}
	}
	err := s.XchgRefs(context.Background(), "srv", "proj", refs)
	require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
}
