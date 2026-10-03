// Copyright 2025 Google LLC
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

package remote

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/objects/gitobjects"
)

func TestNewEntity(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"ValidGroup", "group:mygroup", false},
		{"ValidUser", "user:myuser", false},
		{"InvalidPrefix", "foo:mygroup", true},
		{"NoPrefix", "mygroup", true},
		{"Empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewEntity(tt.value)
			require.Equal(t, tt.wantErr, err != nil)
			if err == nil {
				require.Equal(t, tt.value, got.String())
			}
		})
	}
}

func TestEntity_Set(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		value   string
		wantErr bool
	}{
		{"ValidChange", "group:one", "user:two", false},
		{"InvalidChange", "group:one", "foo:two", true},
		{"SetInvalid", "group:one", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := NewEntity(tt.initial)
			err := got.Set(tt.value)
			require.Equal(t, tt.wantErr, err != nil)
			if err == nil {
				require.Equal(t, tt.value, got.String())
			}
			// We don't want it to change in this case.
			if err != nil {
				require.NotEqual(t, tt.value, got.String())
			}
		})
	}
}

func TestEntity_JSONMarshalUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		entity   Entity
		wantJSON string

		unmarshal        string
		wantUnmarshalErr bool
	}{
		{
			name:             "ValidGroup",
			entity:           Entity{value: "group:test"},
			wantJSON:         `"group:test"`,
			unmarshal:        `"group:test"`,
			wantUnmarshalErr: false,
		},
		{
			name:             "ValidUser",
			entity:           Entity{value: "user:test"},
			wantJSON:         `"user:test"`,
			unmarshal:        `"user:test"`,
			wantUnmarshalErr: false,
		},
		{
			name:             "UnmarshalInvalid",
			unmarshal:        `"invalid:test"`,
			wantUnmarshalErr: true,
		},
		{
			name:             "UnmarshalNotAString",
			unmarshal:        `123`,
			wantUnmarshalErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantJSON != "" {
				got, err := json.Marshal(tt.entity)
				require.NoError(t, err)
				require.Equal(t, tt.wantJSON, string(got))
			}

			if tt.unmarshal != "" {
				var e Entity
				err := json.Unmarshal([]byte(tt.unmarshal), &e)
				require.Equal(t, tt.wantUnmarshalErr, err != nil)
				if err == nil {
					expectedVal := strings.Trim(tt.unmarshal, `"`)
					require.Equal(t, expectedVal, e.String())
				}
			}
		})
	}
}

func mustCreateEntity(t *testing.T, value string) Entity {
	t.Helper()
	e, err := NewEntity(value)
	if err != nil {
		t.Fatalf("Failed to create entity: %v", err)
	}
	return e
}

func sortEntities(entities []Entity) {
	sort.Slice(entities, func(i, j int) bool {
		return entities[i].String() < entities[j].String()
	})
}

func TestACL_AddReaders(t *testing.T) {
	e1 := mustCreateEntity(t, "user:one")
	e2 := mustCreateEntity(t, "group:two")
	e3 := mustCreateEntity(t, "user:three")

	tests := []struct {
		name     string
		initial  []Entity
		toAdd    []Entity
		expected []Entity
	}{
		{"AddEmpty", []Entity{}, []Entity{e1}, []Entity{e1}},
		{"AddExisting", []Entity{e1}, []Entity{e1}, []Entity{e1}},
		{"AddNew", []Entity{e1}, []Entity{e2}, []Entity{e1, e2}},
		{"AddMultiple", []Entity{e1}, []Entity{e2, e3}, []Entity{e1, e2, e3}},
		{"AddMix", []Entity{e1, e2}, []Entity{e2, e3}, []Entity{e1, e2, e3}},
		{"AddToNil", nil, []Entity{e1}, []Entity{e1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acl := ACL{Readers: tt.initial}
			acl.AddReaders(tt.toAdd)
			sortEntities(acl.Readers)
			sortEntities(tt.expected)
			require.Equal(t, tt.expected, acl.Readers)
		})
	}
}

func TestACL_RemoveReaders(t *testing.T) {
	e1 := mustCreateEntity(t, "user:one")
	e2 := mustCreateEntity(t, "group:two")
	e3 := mustCreateEntity(t, "user:three")

	tests := []struct {
		name     string
		initial  []Entity
		toRemove []Entity
		expected []Entity
	}{
		{"RemoveNonExisting", []Entity{e1}, []Entity{e2}, []Entity{e1}},
		{"RemoveExisting", []Entity{e1, e2}, []Entity{e1}, []Entity{e2}},
		{"RemoveMultiple", []Entity{e1, e2, e3}, []Entity{e1, e3}, []Entity{e2}},
		{"RemoveAll", []Entity{e1, e2}, []Entity{e1, e2}, []Entity{}},
		{"RemoveFromNil", nil, []Entity{e1}, []Entity{}},
		{"RemoveEmpty", []Entity{e1}, []Entity{}, []Entity{e1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acl := ACL{Readers: tt.initial}
			acl.RemoveReaders(tt.toRemove)
			sortEntities(acl.Readers)
			sortEntities(tt.expected)
			require.Equal(t, tt.expected, acl.Readers)
		})
	}
}

func TestACL_AddWriters(t *testing.T) {
	e1 := mustCreateEntity(t, "user:one")
	e2 := mustCreateEntity(t, "group:two")

	acl := ACL{}
	acl.AddWriters([]Entity{e1, e2, e1})
	expected := []Entity{e1, e2}
	sortEntities(acl.Writers)
	sortEntities(expected)
	require.Equal(t, expected, acl.Writers)
}

func TestACL_RemoveWriters(t *testing.T) {
	e1 := mustCreateEntity(t, "user:one")
	e2 := mustCreateEntity(t, "group:two")

	acl := ACL{Writers: []Entity{e1, e2}}
	acl.RemoveWriters([]Entity{e1, e1})
	expected := []Entity{e2}
	require.Equal(t, expected, acl.Writers)
}

func TestACL_CanRead(t *testing.T) {
	user1 := mustCreateEntity(t, "user:one")
	user2 := mustCreateEntity(t, "user:two")
	allGroup := mustCreateEntity(t, AllGroup)

	tests := []struct {
		name     string
		acl      ACL
		entity   Entity
		expected bool
	}{
		{"DirectReader", ACL{Readers: []Entity{user1}}, user1, true},
		{"NotAReader", ACL{Readers: []Entity{user1}}, user2, false},
		{"AllGroupInReaders", ACL{Readers: []Entity{allGroup}}, user2, true},
		{"WriterImpliesRead", ACL{Writers: []Entity{user1}}, user1, true},
		{"AllGroupInWritersImpliesRead", ACL{Writers: []Entity{allGroup}}, user2, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.acl.CanRead(tt.entity))
		})
	}
}

func TestACL_CanWrite(t *testing.T) {
	user1 := mustCreateEntity(t, "user:one")
	user2 := mustCreateEntity(t, "user:two")
	allGroup := mustCreateEntity(t, AllGroup)

	tests := []struct {
		name     string
		acl      ACL
		entity   Entity
		expected bool
	}{
		{"DirectWriter", ACL{Writers: []Entity{user1}}, user1, true},
		{"NotAWriter", ACL{Writers: []Entity{user1}}, user2, false},
		{"AllGroupInWriters", ACL{Writers: []Entity{allGroup}}, user2, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.acl.CanWrite(tt.entity))
		})
	}
}

// Mock CASStore implementation for testing ACLEvaluator
type mockCASStore struct {
	// path -> content
	files map[string]string

	entities.CASStore
}

func (m *mockCASStore) OpenBlob(ctx context.Context, hash entities.Hash) (io.ReadCloser, error) {
	content, ok := m.files[hash.String()]
	if !ok {
		return nil, errors.New("blob not found")
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (m *mockCASStore) GetObject(ctx context.Context, hash entities.Hash) ([]byte, error) {
	content, ok := m.files[hash.String()]
	if !ok {
		return nil, errors.New("object not found")
	}
	return []byte(content), nil
}

func gitBlobHash(content string) entities.Hash {
	gitContent := fmt.Sprintf("blob %d\000%s", len(content), content)

	return entities.NewHash(fmt.Sprintf("%x", sha1.Sum([]byte(gitContent))))
}

func mockResolverKey(server, project, path string) string {
	return fmt.Sprintf("%s@%s@%s", server, project, path)
}

// multiProjectMockResolver holds root tree hashes per server/project context and delegates ResolvePath to hashpath package.
type multiProjectMockResolver struct {
	roots map[string]entities.Hash
}

func (r *multiProjectMockResolver) ResolveBase(ctx context.Context, store entities.CASStore, hp *hashpath.HashPath, defaultServer, defaultProject string) (hashpath.BaseDirRef, error) {
	server := hp.Server
	if server == "" {
		server = defaultServer
	}
	project := hp.Project
	if project == "" {
		project = defaultProject
	}
	key := fmt.Sprintf("%s@%s", server, project)
	rootHash, ok := r.roots[key]
	if !ok {
		return hashpath.BaseDirRef{}, entities.NotExistErrf("project %s not found in mock resolver", key)
	}
	return hashpath.BaseDirRef{Base: rootHash, Dir: rootHash, Ref: ACLRef}, nil
}

func (r *multiProjectMockResolver) ResolvePath(ctx context.Context, store entities.CASStore, baseDirRef hashpath.BaseDirRef, hp *hashpath.HashPath) (*hashpath.Resolution, error) {
	return hashpath.ResolvePath(ctx, store, baseDirRef, hp)
}

func TestACLEvaluator_CanAccess(t *testing.T) {
	server := "test-server"
	project := "test-project"
	userReader := mustCreateEntity(t, "user:specific-reader")
	userWriter := mustCreateEntity(t, "user:specific-writer")
	userParent := mustCreateEntity(t, "user:parent-default-reader")
	userParentParent := mustCreateEntity(t, "user:parent-parent-default-reader")
	userAllProjects := mustCreateEntity(t, "user:all-projects-reader")
	userNone := mustCreateEntity(t, "user:none")

	specificACLContent := `{
		"Readers": ["user:specific-reader"],
		"Writers": ["user:specific-writer"]
	}`
	specificACLPath := aclHashPath("refs/heads/main")
	specificACLHash := gitBlobHash(specificACLContent)

	specificACLContentAllGroupReader := `{
		"Readers": ["group:ALL"]
	}`
	specificACLHashAllGroupReader := gitBlobHash(specificACLContentAllGroupReader)

	specificACLContentAllGroupWriter := `{
		"Writers": ["group:ALL"]
	}`
	specificACLHashAllGroupWriter := gitBlobHash(specificACLContentAllGroupWriter)

	parentDefaultACLContent := `{
		"Patterns": {
			"main": {
				"Readers": ["user:parent-default-reader"],
				"Writers": ["user:parent-default-writer"]
			},
			"*": {
				"Readers": ["user:parent-wildcard"]
			}
		}
	}`
	parentDefaultACLPath := aclHashPath("refs/heads/default.json")
	parentDefaultACLHash := gitBlobHash(parentDefaultACLContent)

	parentParentDefaultACLContent := `{
		"Patterns": {
			"heads/main": {
				"Readers": ["user:parent-parent-default-reader"]
			}
		}
	}`
	parentParentDefaultACLPath := aclHashPath("refs/default.json")
	parentParentDefaultACLHash := gitBlobHash(parentParentDefaultACLContent)

	allProjectsACLContent := `{
		"Readers": ["user:all-projects-reader"]
	}`
	allProjectsACLPath := aclHashPath("refs/heads/main")
	allProjectsACLHash := gitBlobHash(allProjectsACLContent)

	tests := []struct {
		name string
		key  ACLQuery

		// hash -> content
		mockCASFiles map[string]string

		// hashpath -> hash
		mockResolutions map[string]entities.Hash

		wantAccess bool
		wantErr    bool
	}{
		{
			name: "Specific ACL grants read",
			key:  ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: userReader},
			mockCASFiles: map[string]string{
				specificACLHash.String(): specificACLContent,
			},
			mockResolutions: map[string]entities.Hash{
				mockResolverKey(server, project, specificACLPath): specificACLHash,
			},
			wantAccess: true,
		},
		{
			name: "Specific ACL grants write",
			key:  ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthWrite, Entity: userWriter},
			mockCASFiles: map[string]string{
				specificACLHash.String(): specificACLContent,
			},
			mockResolutions: map[string]entities.Hash{
				mockResolverKey(server, project, specificACLPath): specificACLHash,
			},
			wantAccess: true,
		},
		{
			name: "ACL without access",
			key:  ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: userNone},
			mockCASFiles: map[string]string{
				specificACLHash.String(): specificACLContent,
			},
			mockResolutions: map[string]entities.Hash{
				mockResolverKey(server, project, specificACLPath): specificACLHash,
			},
			wantAccess: false,
		},
		{
			name: "Parent Default ACL grants read",
			key:  ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: userParent},
			mockCASFiles: map[string]string{
				specificACLHash.String():      specificACLContent,
				parentDefaultACLHash.String(): parentDefaultACLContent,
			},
			mockResolutions: map[string]entities.Hash{
				mockResolverKey(server, project, specificACLPath):      specificACLHash,
				mockResolverKey(server, project, parentDefaultACLPath): parentDefaultACLHash,
			},
			wantAccess: true,
		},
		{
			name: "Parent Parent Default ACL grants read",
			key:  ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: userParentParent},
			mockCASFiles: map[string]string{
				specificACLHash.String():            specificACLContent,
				parentDefaultACLHash.String():       parentDefaultACLContent,
				parentParentDefaultACLHash.String(): parentParentDefaultACLContent,
			},
			mockResolutions: map[string]entities.Hash{
				mockResolverKey(server, project, specificACLPath):            specificACLHash,
				mockResolverKey(server, project, parentDefaultACLPath):       parentDefaultACLHash,
				mockResolverKey(server, project, parentParentDefaultACLPath): parentParentDefaultACLHash,
			},
			wantAccess: true,
		},
		{
			name: "all-projects ACL grants read",
			key:  ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: userAllProjects},
			mockCASFiles: map[string]string{
				allProjectsACLHash.String(): allProjectsACLContent,
			},
			mockResolutions: map[string]entities.Hash{
				mockResolverKey(server, AllProjects, allProjectsACLPath): allProjectsACLHash,
			},
			wantAccess: true,
		},
		{
			name: "AllGroup grants read access",
			key:  ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: userNone},
			mockCASFiles: map[string]string{
				specificACLHashAllGroupReader.String(): specificACLContentAllGroupReader,
			},
			mockResolutions: map[string]entities.Hash{
				mockResolverKey(server, project, specificACLPath): specificACLHashAllGroupReader,
			},
			wantAccess: true,
		},
		{
			name: "AllGroup grants write access",
			key:  ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthWrite, Entity: userNone},
			mockCASFiles: map[string]string{
				specificACLHashAllGroupWriter.String(): specificACLContentAllGroupWriter,
			},
			mockResolutions: map[string]entities.Hash{
				mockResolverKey(server, project, specificACLPath): specificACLHashAllGroupWriter,
			},
			wantAccess: true,
		},
		{
			name:         "No ACL found",
			key:          ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: userNone},
			mockCASFiles: map[string]string{},
			wantAccess:   false,
		},
		{
			name: "Malformed specific ACL JSON",
			key:  ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: userNone},
			mockCASFiles: map[string]string{
				specificACLHash.String(): "{ \"Readers\": [\"invalid\"] }",
			},
			mockResolutions: map[string]entities.Hash{
				mockResolverKey(server, project, specificACLPath): specificACLHash,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, resolver := setupMockStoreAndResolver(t, tt.mockResolutions, tt.mockCASFiles)
			aclEvaluator := NewACLEvaluatorWithResolver(store, resolver)

			hasAccess, err := aclEvaluator.Evaluate(tt.key)

			require.Equal(t, tt.wantErr, err != nil)
			if !tt.wantErr {
				require.Equal(t, tt.wantAccess, hasAccess)
			}
		})
	}
}

func TestPatternedACL_Validate(t *testing.T) {
	tests := []struct {
		name     string
		patterns map[string]ACL
		wantErr  bool
	}{
		{
			name: "ValidPatterns",
			patterns: map[string]ACL{
				"main":      {},
				"feature/*": {},
				"*":         {},
			},
			wantErr: false,
		},
		{
			name: "PrefixWildcard",
			patterns: map[string]ACL{
				"*/v1": {},
			},
			wantErr: true,
		},
		{
			name: "MultipleWildcards",
			patterns: map[string]ACL{
				"feature/*/*": {},
			},
			wantErr: true,
		},
		{
			name: "MiddleWildcard",
			patterns: map[string]ACL{
				"feature/*/main": {},
			},
			wantErr: true,
		},
		{
			name: "DuplicateSeparators",
			patterns: map[string]ACL{
				"feature//main": {},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &PatternedACL{Patterns: tt.patterns}
			err := p.Validate()
			require.Equal(t, tt.wantErr, err != nil)
		})
	}
}

func TestACLEvaluator_PatternPriority(t *testing.T) {
	server := "test-server"
	project := "test-project"
	mainReader := mustCreateEntity(t, "user:main-reader")
	prefixReader := mustCreateEntity(t, "user:prefix-reader")
	prefixMainReader := mustCreateEntity(t, "user:prefix-main-reader")
	prefixLongerReader := mustCreateEntity(t, "user:prefix-longer-reader")
	wildcardReader := mustCreateEntity(t, "user:wildcard-reader")

	parentDefaultACLContent := `{
		"Patterns": {
			"main": {
				"Readers": ["user:main-reader"]
			},
			"prefix*": {
				"Readers": ["user:prefix-reader"]
			},
			"prefix-longer*": {
				"Readers": ["user:prefix-longer-reader"]
			},
			"ma*": {
				"Readers": ["user:prefix-main-reader"]
			},
			"*": {
				"Readers": ["user:wildcard-reader"]
			}
		}
	}`
	parentDefaultACLPath := aclHashPath("refs/heads/default.json")
	parentDefaultACLHash := gitBlobHash(parentDefaultACLContent)
	// hash -> content
	mockCASFiles := map[string]string{
		parentDefaultACLHash.String(): parentDefaultACLContent,
	}
	// hashpath -> hash
	mockResolutions := map[string]entities.Hash{
		mockResolverKey(server, project, parentDefaultACLPath): parentDefaultACLHash,
	}

	tests := []struct {
		name string
		key  ACLQuery

		wantAccess bool
		wantErr    bool
	}{
		{
			name:       "Exact match on main",
			key:        ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: mainReader},
			wantAccess: true,
		},
		{
			name:       "Prefix reader denied on main",
			key:        ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: prefixReader},
			wantAccess: false,
		},
		{
			name:       "Prefix main reader denied on main",
			key:        ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: prefixMainReader},
			wantAccess: false,
		},
		{
			name:       "Prefix reader allowed on prefix-foo",
			key:        ACLQuery{Server: server, Project: project, RefPath: "refs/heads/prefix-foo", AccessType: AuthRead, Entity: prefixReader},
			wantAccess: true,
		},
		{
			name:       "Prefix main reader allowed on main-release",
			key:        ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main-release", AccessType: AuthRead, Entity: prefixMainReader},
			wantAccess: true,
		},
		{
			name:       "Prefix reader denied on prefix-longer-foo",
			key:        ACLQuery{Server: server, Project: project, RefPath: "refs/heads/prefix-longer-foo", AccessType: AuthRead, Entity: prefixReader},
			wantAccess: false,
		},
		{
			name:       "Prefix longer reader allowed on prefix-longer-foo",
			key:        ACLQuery{Server: server, Project: project, RefPath: "refs/heads/prefix-longer-foo", AccessType: AuthRead, Entity: prefixLongerReader},
			wantAccess: true,
		},
		{
			name:       "Wildcard reader denied read main",
			key:        ACLQuery{Server: server, Project: project, RefPath: "refs/heads/main", AccessType: AuthRead, Entity: wildcardReader},
			wantAccess: false,
		},
		{
			name:       "Wildcard reader allowed read banana",
			key:        ACLQuery{Server: server, Project: project, RefPath: "refs/heads/banana", AccessType: AuthRead, Entity: wildcardReader},
			wantAccess: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, resolver := setupMockStoreAndResolver(t, mockResolutions, mockCASFiles)
			aclEvaluator := NewACLEvaluatorWithResolver(store, resolver)

			hasAccess, err := aclEvaluator.Evaluate(tt.key)

			require.Equal(t, tt.wantErr, err != nil)
			if !tt.wantErr {
				require.Equal(t, tt.wantAccess, hasAccess)
			}
		})
	}
}

func TestACLEvaluator_ACLRef(t *testing.T) {
	metaServer := entities.ABFSMetaServer
	metaProject := entities.ABFSMetaProject
	metaACLPath := metaACLHashPath()

	userMetaReader := mustCreateEntity(t, "user:meta-reader")
	userOther := mustCreateEntity(t, "user:other")

	metaACLContent := `{
		"Readers": ["user:meta-reader"]
	}`
	metaACLHash := gitBlobHash(metaACLContent)

	mockCASFiles := map[string]string{
		metaACLHash.String(): metaACLContent,
	}
	mockResolutions := map[string]entities.Hash{
		mockResolverKey(metaServer, metaProject, metaACLPath): metaACLHash,
	}

	tests := []struct {
		name       string
		key        ACLQuery
		wantAccess bool
	}{
		{
			name:       "Meta ACL grants access to 'acl' ref",
			key:        ACLQuery{Server: "any", Project: "any", RefPath: ACLRef, AccessType: AuthRead, Entity: userMetaReader},
			wantAccess: true,
		},
		{
			name:       "Meta ACL denies access to 'acl' ref",
			key:        ACLQuery{Server: "any", Project: "any", RefPath: ACLRef, AccessType: AuthRead, Entity: userOther},
			wantAccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, resolver := setupMockStoreAndResolver(t, mockResolutions, mockCASFiles)
			aclEvaluator := NewACLEvaluatorWithResolver(store, resolver)

			hasAccess, err := aclEvaluator.Evaluate(tt.key)

			require.NoError(t, err)
			require.Equal(t, tt.wantAccess, hasAccess)
		})
	}
}

type snapshotMockResolver struct {
	baseDirRef hashpath.BaseDirRef
}

func (r *snapshotMockResolver) ResolveBase(ctx context.Context, store entities.CASStore, hp *hashpath.HashPath, defaultServer, defaultProject string) (hashpath.BaseDirRef, error) {
	return r.baseDirRef, nil
}

func (r *snapshotMockResolver) ResolvePath(ctx context.Context, store entities.CASStore, baseDirRef hashpath.BaseDirRef, hp *hashpath.HashPath) (*hashpath.Resolution, error) {
	return nil, fmt.Errorf("ResolvePath should not be called in hot path Snapshot Cache")
}

func TestACLEvaluator_SnapshotCache(t *testing.T) {
	file1Hash := entities.NewHash("1111111111111111111111111111111111111111")
	file2Hash := entities.NewHash("2222222222222222222222222222222222222222")

	// Create "refs/heads" subdirectory tree first
	headsTreeEntries := []entities.FileEntry{
		{Name: "main", Mode: entities.FileModeRegular, Hash: file2Hash},
	}
	headsTreeBytes, err := gitobjects.EncodeTree(headsTreeEntries)
	require.NoError(t, err)
	headsTreeHash := entities.NewHash(fmt.Sprintf("%x", sha1.Sum(headsTreeBytes)))

	// Create "refs" subdirectory tree
	refsTreeEntries := []entities.FileEntry{
		{Name: "heads", Mode: entities.FileModeDir, Hash: headsTreeHash},
	}
	refsTreeBytes, err := gitobjects.EncodeTree(refsTreeEntries)
	require.NoError(t, err)
	refsTreeHash := entities.NewHash(fmt.Sprintf("%x", sha1.Sum(refsTreeBytes)))

	// Create root tree
	rootTreeEntries := []entities.FileEntry{
		{Name: "default.json", Mode: entities.FileModeRegular, Hash: file1Hash},
		{Name: "refs", Mode: entities.FileModeDir, Hash: refsTreeHash},
	}
	rootTreeBytes, err := gitobjects.EncodeTree(rootTreeEntries)
	require.NoError(t, err)
	rootTreeHash := entities.NewHash(fmt.Sprintf("%x", sha1.Sum(rootTreeBytes)))

	// Prepare mock raw file contents
	mockFiles := map[string]string{
		rootTreeHash.String():  string(rootTreeBytes),
		refsTreeHash.String():  string(refsTreeBytes),
		headsTreeHash.String(): string(headsTreeBytes),
		file1Hash.String():     `{"patterns": {"*": {"Readers": ["group:ALL"]}}}`,
		file2Hash.String():     `{"Readers": ["user:allowed-user"]}`,
	}
	store := &mockCASStore{files: mockFiles}

	mockRes := &snapshotMockResolver{
		baseDirRef: hashpath.BaseDirRef{Base: rootTreeHash, Dir: rootTreeHash, Ref: ACLRef},
	}

	evaluator := NewACLEvaluatorWithResolver(store, mockRes)

	// Check initial cache state
	snapshotsLen := evaluator.aclSnapshots.Len()
	require.Equal(t, 0, snapshotsLen)

	// Run standard hierarchical permission check
	query := ACLQuery{
		Server:     "server",
		Project:    "project",
		RefPath:    "refs/heads/main",
		AccessType: AuthRead,
		Entity:     mustCreateEntity(t, "user:allowed-user"),
	}

	hasAccess, err := evaluator.Evaluate(query)
	require.NoError(t, err)
	require.True(t, hasAccess)

	// Verify that snapshots map is now populated under project Key
	projKey := serverProjectKey{server: "server", project: "project"}
	item := evaluator.aclSnapshots.Get(projKey)
	require.NotNil(t, item)
	snapshot := item.Value()
	require.NotNil(t, snapshot)

	// Verify snapshot internal maps are parsed correctly
	require.Contains(t, snapshot.ACLs, "refs/heads/main")
	require.Contains(t, snapshot.PatternedACLs, "default.json")
	require.Equal(t, []Entity{mustCreateEntity(t, "user:allowed-user")}, snapshot.ACLs["refs/heads/main"].Readers)

	// Perform a second query that is a cache hit on the snapshot (ResolvePath should NOT be called!)
	query2 := ACLQuery{
		Server:     "server",
		Project:    "project",
		RefPath:    "refs/heads/non-existent",
		AccessType: AuthRead,
		Entity:     mustCreateEntity(t, "user:any-user"),
	}
	hasAccess2, err := evaluator.Evaluate(query2)
	require.NoError(t, err)
	require.True(t, hasAccess2, "Should fall back to default.json global wildcard group:ALL")
}

func setupMockStoreAndResolver(t *testing.T, mockResolutions map[string]entities.Hash, mockCASFiles map[string]string) (entities.CASStore, HashpathResolver) {
	// Group input mock files by project key "server@project"
	projectFilesMap := make(map[string]map[string]string)

	for resKey, fileHash := range mockResolutions {
		parts := strings.SplitN(resKey, "@", 3)
		if len(parts) < 3 {
			continue
		}
		server, project, aclPath := parts[0], parts[1], parts[2]

		path := strings.TrimPrefix(aclPath, ACLRef+":")
		content, ok := mockCASFiles[fileHash.String()]
		if !ok {
			continue
		}

		projKey := fmt.Sprintf("%s@%s", server, project)
		if _, exists := projectFilesMap[projKey]; !exists {
			projectFilesMap[projKey] = make(map[string]string)
		}
		projectFilesMap[projKey][path] = content
	}

	// hash -> content
	casFiles := make(map[string]string)

	// server@project -> treehash
	roots := make(map[string]entities.Hash)

	for projKey, files := range projectFilesMap {
		roots[projKey] = buildMockTree(t, files, casFiles)
	}

	store := &mockCASStore{files: casFiles}
	resolver := &multiProjectMockResolver{roots: roots}
	return store, resolver
}

func buildMockTree(t *testing.T, files map[string]string, casFiles map[string]string) entities.Hash {
	type mockNode struct {
		isDir    bool
		hash     entities.Hash
		content  string
		children map[string]*mockNode
	}

	root := &mockNode{isDir: true, children: make(map[string]*mockNode)}

	for path, content := range files {
		parts := strings.Split(path, "/")
		curr := root
		for i, part := range parts {
			isLast := i == len(parts)-1
			child, exists := curr.children[part]
			if !exists {
				child = &mockNode{
					isDir:    !isLast,
					children: make(map[string]*mockNode),
				}
				curr.children[part] = child
			}
			if isLast {
				child.content = content
				child.hash = gitBlobHash(content)
				casFiles[child.hash.String()] = content
			}
			curr = child
		}
	}

	var encodeNode func(node *mockNode)
	encodeNode = func(node *mockNode) {
		if !node.isDir {
			return
		}

		var entries []entities.FileEntry
		for name, child := range node.children {
			encodeNode(child)
			mode := entities.FileModeRegular
			if child.isDir {
				mode = entities.FileModeDir
			}
			entries = append(entries, entities.FileEntry{
				Name: name,
				Mode: mode,
				Hash: child.hash,
			})
		}

		treeBytes, err := gitobjects.EncodeTree(entries)
		require.NoError(t, err)
		node.hash = entities.NewHash(fmt.Sprintf("%x", sha1.Sum(treeBytes)))
		casFiles[node.hash.String()] = string(treeBytes)
	}

	encodeNode(root)
	return root.hash
}

type countResolver struct {
	resolveBaseCalls int
}

func (c *countResolver) ResolveBase(ctx context.Context, store entities.CASStore, hp *hashpath.HashPath, defaultServer, defaultProject string) (hashpath.BaseDirRef, error) {
	c.resolveBaseCalls++
	return hashpath.BaseDirRef{}, entities.ErrNotExist
}

func (c *countResolver) ResolvePath(ctx context.Context, store entities.CASStore, baseDirRef hashpath.BaseDirRef, hp *hashpath.HashPath) (*hashpath.Resolution, error) {
	return nil, nil
}

func TestACLEvaluator_NegativeCaching(t *testing.T) {
	store := &mockCASStore{files: make(map[string]string)}
	resolver := &countResolver{}
	evaluator := NewACLEvaluatorWithResolver(store, resolver)

	query1 := ACLQuery{
		Server:     "server",
		Project:    "non-existent-project",
		RefPath:    "refs/heads/pathA",
		AccessType: AuthRead,
		Entity:     mustCreateEntity(t, "user:any"),
	}

	// First query: should call ResolveBase twice (once for non-existent-project and once for AllProjects)
	// and return access denied (false, nil)
	hasAccess1, err := evaluator.Evaluate(query1)
	require.NoError(t, err)
	require.False(t, hasAccess1)
	require.Equal(t, 2, resolver.resolveBaseCalls)

	// Second query (different path, same project): should HIT the negative cache of snapshots and not call ResolveBase again
	query2 := ACLQuery{
		Server:     "server",
		Project:    "non-existent-project",
		RefPath:    "refs/heads/pathB",
		AccessType: AuthRead,
		Entity:     mustCreateEntity(t, "user:any"),
	}
	hasAccess2, err := evaluator.Evaluate(query2)
	require.NoError(t, err)
	require.False(t, hasAccess2)
	require.Equal(t, 2, resolver.resolveBaseCalls) // count should still be 2!
}
