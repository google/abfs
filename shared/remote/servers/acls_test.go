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

package servers_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers"
	"github.com/google/abfs/shared/remote/servers/inmemory"
	abfsstore "github.com/google/abfs/store"
)

type aclEntry struct {
	Path string `json:"path"`

	// Valid when Path ends with 'default.json'.
	Patterns map[string]remote.ACL `json:"patterns,omitempty"`

	remote.ACL
}

type serverProjectACLs struct {
	Server  string     `json:"server"`
	Project string     `json:"project"`
	ACLs    []aclEntry `json:"acls"`
}

type testNode struct {
	isDir    bool
	data     []byte
	children map[string]*testNode
}

func loadTestTree(ctx context.Context, store entities.CASStore, hash entities.Hash) (*testNode, error) {
	hdr, err := store.ObjectHeader(ctx, hash)
	if err != nil {
		return nil, err
	}
	n := &testNode{}
	if hdr.Type == entities.ObjectType(gitobjects.ObjectTypeDir) {
		n.isDir = true
		n.children = make(map[string]*testNode)
		buf, _, err := store.GetTree(ctx, hash)
		if err != nil {
			return nil, err
		}
		entries, err := entities.DecodeDirPlain(buf)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			child, err := loadTestTree(ctx, store, entry.Hash)
			if err != nil {
				return nil, err
			}
			n.children[entry.Name] = child
		}
	} else {
		n.isDir = false
		data, err := abfsstore.ReadAllBlob(ctx, store, hash)
		if err != nil {
			return nil, err
		}
		n.data = data
	}
	return n, nil
}

func TestLoadAndSetACLs(t *testing.T) {
	ctx := context.Background()

	// Initialize in-memory server
	inmem := inmemory.New()

	// Setup a temporary file for the ACLs JSON
	tmpFile, err := os.CreateTemp("", "acls-*.json")
	require.NoError(t, err)
	defer func() {
		err := os.Remove(tmpFile.Name())
		require.NoError(t, err)
	}()

	// Setup test ACLs
	entALL, err := remote.NewEntity("group:ALL")
	require.NoError(t, err)

	entUser, err := remote.NewEntity("user:test-user@example.com")
	require.NoError(t, err)

	acls := []serverProjectACLs{
		{
			Server:  "test-server.com",
			Project: "test-project",
			ACLs: []aclEntry{
				{
					Path: "refs/heads/default.json",
					Patterns: map[string]remote.ACL{
						"*": {
							Readers: []remote.Entity{entALL},
						},
					},
				},
				{
					Path: "refs/heads/main.json",
					ACL: remote.ACL{
						Readers: []remote.Entity{entUser},
					},
				},
			},
		},
	}

	content, err := json.Marshal(acls)
	require.NoError(t, err)

	_, err = tmpFile.Write(content)
	require.NoError(t, err)
	err = tmpFile.Close()
	require.NoError(t, err)

	// Run the load and set logic
	err = servers.LoadAndSetACLs(ctx, inmem, tmpFile.Name())
	require.NoError(t, err)

	// Verify that the ACLs ref was updated on the server
	hashes, err := inmem.GetRefs(ctx, "test-server.com", "test-project", []string{remote.ACLRef})
	require.NoError(t, err)
	require.Len(t, hashes, 1)
	require.NotEqual(t, entities.ZeroHash, hashes[0])

	// Wrap in RemoteStore to verify content can be read back
	store := remote.NewRemoteStore(remote.NewCompressingCloser(remote.NopCloser{RemoteProvider: inmem}))
	rootNode, err := loadTestTree(ctx, store, hashes[0])
	require.NoError(t, err)

	// Verify default.json
	defaultFile := rootNode.children["refs"].children["heads"].children["default.json"]
	require.NotNil(t, defaultFile)
	require.False(t, defaultFile.isDir)

	var pacl remote.PatternedACL
	err = json.Unmarshal(defaultFile.data, &pacl)
	require.NoError(t, err)
	require.Contains(t, pacl.Patterns, "*")
	require.Equal(t, "group:ALL", pacl.Patterns["*"].Readers[0].String())

	// Verify main.json
	mainFile := rootNode.children["refs"].children["heads"].children["main.json"]
	require.NotNil(t, mainFile)
	require.False(t, mainFile.isDir)

	var acl remote.ACL
	err = json.Unmarshal(mainFile.data, &acl)
	require.NoError(t, err)
	require.Equal(t, "user:test-user@example.com", acl.Readers[0].String())
}

func TestACLServer_PutRefsCacheInvalidation(t *testing.T) {
	ctx := context.Background()
	inmem := inmemory.New()

	adminUser := remote.User{Email: "admin@example.com"}
	adminEnt, err := remote.NewEntity("user:admin@example.com")
	require.NoError(t, err)

	entALL, err := remote.NewEntity("group:ALL")
	require.NoError(t, err)

	// Step 1: Bootstrap the initial configuration (abfs-meta-acl) on raw backing server
	// This grants the admin user access to write all ACLs.
	metaACLs := []serverProjectACLs{
		{
			Server:  entities.ABFSMetaServer,
			Project: entities.ABFSMetaProject,
			ACLs: []aclEntry{
				{
					Path: remote.MetaACLFile,
					ACL: remote.ACL{
						Writers: []remote.Entity{adminEnt},
						Readers: []remote.Entity{adminEnt},
					},
				},
			},
		},
	}

	tmpFile1, err := os.CreateTemp("", "acls-1-*.json")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpFile1.Name()) }()
	content1, err := json.Marshal(metaACLs)
	require.NoError(t, err)
	_, err = tmpFile1.Write(content1)
	require.NoError(t, err)
	require.NoError(t, tmpFile1.Close())

	err = servers.LoadAndSetACLs(ctx, inmem, tmpFile1.Name())
	require.NoError(t, err)

	// Step 2: Create ACLServer and ACLEvaluator wrapping the server
	store := remote.NewRemoteStore(remote.NewCompressingCloser(remote.NopCloser{RemoteProvider: inmem}))
	evaluator := remote.NewACLEvaluator(store)
	aclServer := servers.NewACLServer(inmem, evaluator)

	// Step 3: Query map ACLs (should return false because abfs-maps does not exist yet)
	hasAccess, err := evaluator.EvaluateMapACLs(adminEnt, remote.AuthWrite)
	require.NoError(t, err)
	require.False(t, hasAccess)

	// Step 4: Perform update to abfs-maps by loading new ACLs through the aclServer
	// with the admin user in context.
	ctxWithAdmin := remote.NewContextWithUser(ctx, adminUser)

	mapsACLs := []serverProjectACLs{
		{
			Server:  entities.ABFSMetaServer,
			Project: entities.ABFSMetaProject,
			ACLs: []aclEntry{
				{
					Path: remote.MetaACLFile,
					ACL: remote.ACL{
						Writers: []remote.Entity{adminEnt},
						Readers: []remote.Entity{adminEnt},
					},
				},
				{
					Path: remote.MapsACLFile,
					ACL: remote.ACL{
						Writers: []remote.Entity{entALL},
					},
				},
			},
		},
	}

	tmpFile2, err := os.CreateTemp("", "acls-2-*.json")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpFile2.Name()) }()
	content2, err := json.Marshal(mapsACLs)
	require.NoError(t, err)
	_, err = tmpFile2.Write(content2)
	require.NoError(t, err)
	require.NoError(t, tmpFile2.Close())

	err = servers.LoadAndSetACLs(ctx, inmem, tmpFile2.Name())
	require.NoError(t, err)

	hashes, err := inmem.GetRefs(ctx, entities.ABFSMetaServer, entities.ABFSMetaProject, []string{remote.ACLRef})
	require.NoError(t, err)
	require.Len(t, hashes, 1)

	err = aclServer.PutRefs(ctxWithAdmin, entities.ABFSMetaServer, entities.ABFSMetaProject, []entities.RefHash{
		{Ref: remote.ACLRef, Hash: hashes[0]},
	})
	require.NoError(t, err)

	// Step 5: Query map ACLs again! Because the cache was invalidated, it should now return true!
	hasAccess, err = evaluator.EvaluateMapACLs(adminEnt, remote.AuthWrite)
	require.NoError(t, err)
	require.True(t, hasAccess)
}
