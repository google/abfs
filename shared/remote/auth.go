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

package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/store"
)

const (
	aclTTLDuration = 10 * time.Minute

	topLevelCacheCapacity  = 50000
	projectCacheCapacity   = 50000
	snapshotsCacheCapacity = 1000

	// Fallback project for wider ACLs.
	AllProjects = "abfs-all-projects"

	// File with patterns for wildcarding.
	DefaultACLFile = "default.json"

	// Permissions to access ACLs.
	// acl@ABFSMetaProject@ABFSMetaServer:abfs-meta-acl
	MetaACLFile = "abfs-meta-acl"

	// Permissions to access Maps.
	// acl@ABFSMetaProject@ABFSMetaServer:abfs-maps
	MapsACLFile = "abfs-maps"

	// Ref namespace for ACLs.
	ACLRef = "acl"

	// Sentinel allow all group
	AllGroup = "group:ALL"
)

type AccessType int

const (
	AuthRead AccessType = iota
	AuthWrite
)

func (a AccessType) String() string {
	switch a {
	case AuthRead:
		return "read"
	case AuthWrite:
		return "write"
	default:
		return "unknown"
	}
}

type userKey struct{}

// User represents an authenticated user.
type User struct {
	Email   string
	Subject string
}

// NewContextWithUser returns a new context with the given user.
func NewContextWithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

// UserFromContext returns the user from the context, if present.
func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userKey{}).(User)
	return user, ok
}

// Entity is the unit of granularity that can have
// ACLs applied. Examples are...
// user:foo@domain.com
// group:groupName@example.com
type Entity struct {
	value string
}

const (
	GroupPrefix = "group:"
	UserPrefix  = "user:"
)

// NewEntity creates a new Entity after validating the format.
func NewEntity(value string) (Entity, error) {
	if !strings.HasPrefix(value, GroupPrefix) && !strings.HasPrefix(value, UserPrefix) {
		return Entity{}, fmt.Errorf("invalid entity format: %s. Entity must start with '%s' or '%s'", value, GroupPrefix, UserPrefix)
	}
	return Entity{value: value}, nil
}

// Set validates and sets the Entity value.
func (e *Entity) Set(value string) error {
	if !strings.HasPrefix(value, GroupPrefix) && !strings.HasPrefix(value, UserPrefix) {
		return fmt.Errorf("invalid entity format: %s. Entity must start with '%s' or '%s'", value, GroupPrefix, UserPrefix)
	}
	e.value = value
	return nil
}

// String returns the string value of the Entity.
func (e Entity) String() string {
	return e.value
}

// MarshalJSON implements the json.Marshaler interface.
func (e Entity) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.value)
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (e *Entity) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return e.Set(value)
}

// ACL is the structured form of ACL data present in the CAS Store. It's
// applicable for direct mirrors of a reference:
// e.g.
//
//	ref -> 'refs/heads/main'
//	acl -> 'acl:refs/heads/main'
type ACL struct {
	Readers []Entity
	Writers []Entity
}

// PatternedACL is the structured form of ACL data present in the CAS Store. It's
// applicable for the wide, fallback ACLs found at 'default.json'.
// e.g.
//
//	ref -> 'refs/heads/main'
//	acl -> 'acl:refs/heads/default.json'
type PatternedACL struct {
	Patterns map[string]ACL `json:"patterns"`
}

// Validate checks that patterns follow the supported rules:
// 1. No multiple wildcards.
// 2. No middle wildcards (must be suffix).
// 3. No duplicate '/' separators.
func (p *PatternedACL) Validate() error {
	for pattern := range p.Patterns {
		if strings.Contains(pattern, "//") {
			return fmt.Errorf("invalid pattern %q: duplicate '/' separators", pattern)
		}
		wildcardCount := strings.Count(pattern, "*")
		if wildcardCount > 1 {
			return fmt.Errorf("invalid pattern %q: multiple wildcards not supported", pattern)
		}
		if wildcardCount == 1 {
			if len(pattern) > 1 && !strings.HasSuffix(pattern, "*") {
				return fmt.Errorf("invalid pattern %q: wildcard must be at the end", pattern)
			}
		}
	}
	return nil
}

func addEntities(current []Entity, toAdd []Entity) []Entity {
	existing := make(map[Entity]bool)
	for _, entity := range current {
		existing[entity] = true
	}

	for _, entity := range toAdd {
		if !existing[entity] {
			current = append(current, entity)
			existing[entity] = true
		}
	}
	return current
}

func removeEntities(current []Entity, toRemove []Entity) []Entity {
	removeSet := make(map[Entity]bool)
	for _, entity := range toRemove {
		removeSet[entity] = true
	}

	result := []Entity{}
	for _, entity := range current {
		if !removeSet[entity] {
			result = append(result, entity)
		}
	}
	return result
}

// AddReaders adds entities to the Readers list, ensuring no duplicates.
func (acl *ACL) AddReaders(entities []Entity) {
	acl.Readers = addEntities(acl.Readers, entities)
}

// RemoveReaders removes entities from the Readers list.
func (acl *ACL) RemoveReaders(entities []Entity) {
	acl.Readers = removeEntities(acl.Readers, entities)
}

// AddWriters adds entities to the Writers list, ensuring no duplicates.
func (acl *ACL) AddWriters(entities []Entity) {
	acl.Writers = addEntities(acl.Writers, entities)
}

// RemoveWriters removes entities from the Writers list.
func (acl *ACL) RemoveWriters(entities []Entity) {
	acl.Writers = removeEntities(acl.Writers, entities)
}

// TODO: Add group membership checks
func (acl *ACL) CanRead(entity Entity) bool {
	for _, reader := range acl.Readers {
		if entity == reader {
			return true
		}

		if reader.String() == AllGroup {
			return true
		}
	}

	// Write-implies-read
	return acl.CanWrite(entity)
}

// TODO: Add group membership checks
func (acl *ACL) CanWrite(entity Entity) bool {
	for _, writer := range acl.Writers {
		if entity == writer {
			return true
		}

		if writer.String() == AllGroup {
			return true
		}
	}

	return false
}

// ACLQuery is the payload issued to evaluate whether an entity has access.
type ACLQuery struct {
	Server  string
	Project string

	// RefPath is normally a ref, such as 'refs/heads/main'. This is resolved
	// with a hierarchical search in the 'acl' refspace.
	// When used in Meta searches (acl refs, maps, etc...), this is a relative
	// path from the directory which the 'acl' ref points. This is resolved
	// with a single, flat search at the path.
	RefPath string
	Entity  Entity
	AccessType
}

type HashpathResolver interface {
	ResolveBase(ctx context.Context, store entities.CASStore, hp *hashpath.HashPath, defaultServer, defaultProject string) (hashpath.BaseDirRef, error)
	ResolvePath(ctx context.Context, store entities.CASStore, baseDirRef hashpath.BaseDirRef, hp *hashpath.HashPath) (*hashpath.Resolution, error)
}

type defaultResolver struct{}

func (d defaultResolver) ResolveBase(ctx context.Context, store entities.CASStore, hp *hashpath.HashPath, defaultServer, defaultProject string) (hashpath.BaseDirRef, error) {
	return hashpath.ResolveBase(ctx, store, hp, defaultServer, defaultProject)
}

func (d defaultResolver) ResolvePath(ctx context.Context, store entities.CASStore, baseDirRef hashpath.BaseDirRef, hp *hashpath.HashPath) (*hashpath.Resolution, error) {
	return hashpath.ResolvePath(ctx, store, baseDirRef, hp)
}

type serverProjectKey struct {
	server  string
	project string
}

type ACLEvaluator struct {
	// cache caches the lookup of the entire top-level query
	// e.g. the main project + hierarchy walk AND
	// abfs-all-projects + hierarchy walk
	// This operates at the Remote API level.
	// cache is stale (until TTL expiration) if:
	// 1. group membership changes
	// 2. ACLs in CAS store change
	cache *ttlcache.Cache[ACLQuery, bool]

	// projectCache caches the lookup of individual project query
	// e.g. the main project + hierarchy walk OR
	// abfs-all-projects + hierarchy walk
	// cache is stale (until TTL expiration) if:
	// 1. group membership changes
	// 2. ACLs in CAS store change
	projectCache *ttlcache.Cache[ACLQuery, bool]

	// aclSnapshots holds parsed whole-project ACL aclSnapshots
	aclSnapshots *ttlcache.Cache[serverProjectKey, *ACLSnapshot]

	store    entities.CASStore
	resolver HashpathResolver
}

func NewACLEvaluator(store entities.CASStore) *ACLEvaluator {
	return NewACLEvaluatorWithResolver(store, defaultResolver{})
}

func NewACLEvaluatorWithResolver(store entities.CASStore, resolver HashpathResolver) *ACLEvaluator {
	eval := &ACLEvaluator{
		cache: ttlcache.New(
			ttlcache.WithTTL[ACLQuery, bool](aclTTLDuration),
			ttlcache.WithCapacity[ACLQuery, bool](topLevelCacheCapacity),
			ttlcache.WithDisableTouchOnHit[ACLQuery, bool](),
		),
		projectCache: ttlcache.New(
			ttlcache.WithTTL[ACLQuery, bool](aclTTLDuration),
			ttlcache.WithCapacity[ACLQuery, bool](projectCacheCapacity),
			ttlcache.WithDisableTouchOnHit[ACLQuery, bool](),
		),
		aclSnapshots: ttlcache.New(
			ttlcache.WithTTL[serverProjectKey, *ACLSnapshot](aclTTLDuration),
			ttlcache.WithCapacity[serverProjectKey, *ACLSnapshot](snapshotsCacheCapacity),
			ttlcache.WithDisableTouchOnHit[serverProjectKey, *ACLSnapshot](),
		),
		store:    store,
		resolver: resolver,
	}
	go eval.cache.Start()
	go eval.projectCache.Start()
	go eval.aclSnapshots.Start()
	return eval
}

func (a *ACLEvaluator) DropCaches() {
	eg := errgroup.Group{}
	eg.Go(func() error { a.cache.DeleteAll(); return nil })
	eg.Go(func() error { a.projectCache.DeleteAll(); return nil })
	eg.Go(func() error { a.aclSnapshots.DeleteAll(); return nil })

	_ = eg.Wait() // no errors
}

func (a *ACLEvaluator) Resolve(ctx context.Context, store entities.CASStore, hp *hashpath.HashPath, defaultServer, defaultProject string) (*hashpath.Resolution, error) {
	if hp.IsSimpleHash() {
		return &hashpath.Resolution{
			Base:     hp.Hash,
			Resolved: hp.Hash,
		}, nil
	}

	bdr, err := a.resolver.ResolveBase(ctx, store, hp, defaultServer, defaultProject)
	if err != nil {
		return nil, err
	}

	return a.resolver.ResolvePath(ctx, store, bdr, hp)
}

func mapACLHashPath() string {
	return ACLRef + ":" + MapsACLFile
}

func metaACLHashPath() string {
	return ACLRef + ":" + MetaACLFile
}

func aclHashPath(path string) string {
	return ACLRef + ":" + path
}

// evaluateMetaACL searches for ACLs defined in the meta server and project's
// 'acl' ref space. No hierarchy or fallback is searched.
func (a *ACLEvaluator) evaluateMetaACL(entity Entity, accessType AccessType, hashPath string) (bool, error) {
	// Route evaluation to the meta server and project.
	key := ACLQuery{
		Server:     entities.ABFSMetaServer,
		Project:    entities.ABFSMetaProject,
		RefPath:    hashPath,
		Entity:     entity,
		AccessType: accessType,
	}

	item := a.cache.Get(key)
	if item != nil {
		return item.Value(), nil
	}

	ctx := context.Background()
	hasAccess, err := a.evaluateMetaACLInternal(ctx, key)
	if err != nil {
		return false, err
	}

	a.cache.Set(key, hasAccess, ttlcache.DefaultTTL)
	return hasAccess, nil
}

func (a *ACLEvaluator) evaluateMetaACLInternal(ctx context.Context, key ACLQuery) (bool, error) {
	snapshot, err := a.getOrLoadSnapshot(ctx, key.Server, key.Project)
	if err != nil {
		if entities.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	// Parse the path to get raw filename
	path := strings.TrimPrefix(key.RefPath, ACLRef+":")

	if acl, ok := snapshot.ACLs[path]; ok {
		return checkAccess(acl, key.Entity, key.AccessType), nil
	}

	return false, nil
}

// evaluateACLsonACLs checks if a user is able to read or write an 'acl' ref.
func (a *ACLEvaluator) evaluateACLsonACLs(entity Entity, accessType AccessType) (bool, error) {
	return a.evaluateMetaACL(entity, accessType, metaACLHashPath())
}

// EvaluateMapACLs checks if a user is able to read or write map results.
func (a *ACLEvaluator) EvaluateMapACLs(entity Entity, accessType AccessType) (bool, error) {
	return a.evaluateMetaACL(entity, accessType, mapACLHashPath())
}

// Evaluate returns true if the entity passes the ACL check for a ref.
// Hierarchy and the meta project may be searched.
func (a *ACLEvaluator) Evaluate(key ACLQuery) (bool, error) {
	// ACLs on ACLs are handled separately.
	if key.RefPath == ACLRef {
		return a.evaluateACLsonACLs(key.Entity, key.AccessType)
	}

	item := a.cache.Get(key)
	if item != nil {
		return item.Value(), nil
	}

	ctx := context.Background()
	hasAccess, err := a.handleHierarchicalQuery(ctx, key)
	if err != nil {
		return false, err
	}

	a.cache.Set(key, hasAccess, ttlcache.DefaultTTL)
	return hasAccess, nil
}

func (a *ACLEvaluator) checkProjectAndHierarchy(ctx context.Context, key ACLQuery, project string) (bool, error) {
	checkKey := key
	checkKey.Project = project
	if item := a.projectCache.Get(checkKey); item != nil {
		return item.Value(), nil
	}

	hasAccess, err := a.checkProjectAndHierarchyInternal(ctx, key, project)
	if err != nil {
		return false, err
	}

	a.projectCache.Set(checkKey, hasAccess, ttlcache.DefaultTTL)
	return hasAccess, nil
}

func (a *ACLEvaluator) checkProjectAndHierarchyInternal(ctx context.Context, key ACLQuery, project string) (bool, error) {
	snapshot, err := a.getOrLoadSnapshot(ctx, key.Server, project)
	if err != nil {
		if entities.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	return a.evaluateWithSnapshot(key, snapshot), nil
}

func (a *ACLEvaluator) handleHierarchicalQuery(ctx context.Context, key ACLQuery) (bool, error) {
	// Check specific project first.
	if hasAccess, err := a.checkProjectAndHierarchy(ctx, key, key.Project); err != nil {
		return false, err
	} else {
		if hasAccess {
			return true, nil
		}
	}

	// Fallback to all projects.
	if hasAccess, err := a.checkProjectAndHierarchy(ctx, key, AllProjects); err != nil {
		return false, err
	} else {
		if hasAccess {
			return true, nil
		}
	}

	// Access is denied.
	return false, nil
}

func checkAccess(acl *ACL, entity Entity, accessType AccessType) bool {
	if acl == nil {
		return false
	}
	switch accessType {
	case AuthRead:
		return acl.CanRead(entity)
	case AuthWrite:
		return acl.CanWrite(entity)
	default:
		return false
	}
}

// ACLSnapshot is an in-memory snapshot of the ACL tree for
// a (sever, project) tuple.
type ACLSnapshot struct {
	TreeHash      entities.Hash
	ACLs          map[string]*ACL
	PatternedACLs map[string]*PatternedACL
}

var notExistSnapshot = &ACLSnapshot{}

func (a *ACLEvaluator) getOrLoadSnapshot(ctx context.Context, server, project string) (*ACLSnapshot, error) {
	key := serverProjectKey{server: server, project: project}

	// Early exit if found in cache.
	if item := a.aclSnapshots.Get(key); item != nil {
		val := item.Value()
		if val == notExistSnapshot {
			return nil, entities.ErrNotExist
		}
		return val, nil
	}

	// Find 'acl' ref latest treehash through CAS.
	hp := &hashpath.HashPath{Ref: ACLRef}
	bdr, err := a.resolver.ResolveBase(ctx, a.store, hp, server, project)
	if err != nil {
		if entities.IsNotExist(err) {
			a.aclSnapshots.Set(key, notExistSnapshot, ttlcache.DefaultTTL)
			return nil, entities.ErrNotExist
		}
		return nil, err
	}
	currentTreeHash := bdr.Dir

	// Materialize the acl tree.
	loadedSnap, err := a.loadSnapshot(ctx, currentTreeHash)
	if err != nil {
		return nil, err
	}
	loadedSnap.TreeHash = currentTreeHash

	// Save it.
	a.aclSnapshots.Set(key, loadedSnap, ttlcache.DefaultTTL)
	return loadedSnap, nil
}

// loadSnapshot materializes the acl treeHash into memory.
func (a *ACLEvaluator) loadSnapshot(ctx context.Context, treeHash entities.Hash) (*ACLSnapshot, error) {
	snapshot := &ACLSnapshot{
		ACLs:          make(map[string]*ACL),
		PatternedACLs: make(map[string]*PatternedACL),
	}
	err := a.parseSnapshotTree(ctx, treeHash, "", snapshot)
	return snapshot, err
}

// parseSnapshotTree performs the tree walk of treeHash to materialize
// the ACL snapshot.
func (a *ACLEvaluator) parseSnapshotTree(ctx context.Context, treeHash entities.Hash, prefix string, snapshot *ACLSnapshot) error {
	content, err := a.store.GetObject(ctx, treeHash)
	if err != nil {
		return err
	}
	entries, err := entities.DecodeDirPlain(content)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fullPath := entry.Name
		if prefix != "" {
			fullPath = prefix + "/" + entry.Name
		}

		if entry.Mode.IsDir() {
			if err := a.parseSnapshotTree(ctx, entry.Hash, fullPath, snapshot); err != nil {
				return err
			}
		} else {
			fileBytes, err := store.ReadAllBlob(ctx, a.store, entry.Hash)
			if err != nil {
				return err
			}

			if entry.Name == DefaultACLFile {
				pACL := &PatternedACL{}
				if err := json.Unmarshal(fileBytes, pACL); err != nil {
					return fmt.Errorf("failed to parse patterned ACL at %s: %w", fullPath, err)
				}
				if err := pACL.Validate(); err != nil {
					return err
				}
				snapshot.PatternedACLs[fullPath] = pACL
			} else {
				acl := &ACL{}
				if err := json.Unmarshal(fileBytes, acl); err != nil {
					return fmt.Errorf("failed to parse exact ACL at %s: %w", fullPath, err)
				}
				snapshot.ACLs[fullPath] = acl
			}
		}
	}
	return nil
}

func (a *ACLEvaluator) evaluateWithSnapshot(key ACLQuery, snapshot *ACLSnapshot) bool {
	// 1. Exact match.
	if acl, ok := snapshot.ACLs[key.RefPath]; ok {
		if checkAccess(acl, key.Entity, key.AccessType) {
			return true
		}
	}

	// 2. Traversal of parent defaults.
	refParts := strings.Split(key.RefPath, "/")
	for i := len(refParts) - 1; i >= 0; i-- {
		parentRef := strings.Join(refParts[0:i], "/")
		parentACLPath := parentRef + "/" + DefaultACLFile
		if parentRef == "" {
			parentACLPath = DefaultACLFile
		}

		if patternedACL, ok := snapshot.PatternedACLs[parentACLPath]; ok {
			relRef := strings.Join(refParts[i:], "/")
			acl := matchPattern(patternedACL.Patterns, relRef)
			if acl != nil {
				if checkAccess(acl, key.Entity, key.AccessType) {
					return true
				}
			}
		}
	}

	return false
}

// Priority:
// 1. Exact match.
// 2. Longest prefix match (feature*).
// 3. Global wildcard (*).
// Note that we do not support suffix, middle or multiple wildcards in a single entry.
func matchPattern(patterns map[string]ACL, relRef string) *ACL {
	// 1. Exact match
	if acl, ok := patterns[relRef]; ok {
		return &acl
	}

	var (
		prefixACL        *ACL
		bestPrefixLength = -1
	)

	for pattern, acl := range patterns {
		if pattern == "*" {
			continue
		}

		isPrefix := strings.HasSuffix(pattern, "*")

		if isPrefix {
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(relRef, prefix) {
				if len(prefix) > bestPrefixLength {
					bestPrefixLength = len(prefix)
					// Safe since the loop variable is unique per iteration
					prefixACL = &acl
				}
			}
		}
	}

	// 2. Longest prefix match
	if bestPrefixLength > 0 {
		return prefixACL
	}

	// 3. Global wildcard
	if acl, ok := patterns["*"]; ok {
		return &acl
	}

	return nil
}
