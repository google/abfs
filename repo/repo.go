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

package repo

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/google/abfs/manifest"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/store"
)

const ABFSDirHashTag = "abfs.dir.hash.tag"

type projectRefURLMap map[string]entities.Hash
type projectRefMap map[string]projectRefURLMap
type projectMap map[string]projectRefMap

type remoteData struct {
	fetchSchemeURL string
	revision       string
}

type CASGit interface {
	GetCommit(ctx context.Context, key entities.Hash) (*object.Commit, error)
	ResolveCommit(ctx context.Context, key entities.Hash) (*object.Commit, error)
}

type Repo struct {
	store               entities.CASStore
	git                 CASGit
	overrides           map[string]entities.Hash
	manifest            *manifest.Manifest
	pinned              *manifest.Manifest
	manifestProjectName string
	manifestServer      string
	manifestScheme      string
	localManifestDir    string

	remoteCache           map[string]remoteData
	defaultSchemeURL      string
	ignoreMissingProjects bool

	manifestSet bool
	rootHash    entities.Hash
}

func New(store entities.CASStore, git CASGit, manifestServer, manifestProjectName, manifestScheme, localManifestDir string, ignoreMissingProjects bool) *Repo {
	return &Repo{
		store:                 store,
		git:                   git,
		overrides:             make(projectRefURLMap),
		manifestProjectName:   manifestProjectName,
		manifestServer:        instance.MaybeRemoveScheme(manifestServer),
		manifestScheme:        manifestScheme,
		localManifestDir:      localManifestDir,
		remoteCache:           make(map[string]remoteData),
		ignoreMissingProjects: ignoreMissingProjects,
	}
}

// SchemeURLForRemote returns the SchemeURL for a named remote. Should only be called
// after manifest is set.
func (r *Repo) SchemeURLForRemote(remote string) (string, error) {
	if !r.manifestSet {
		return "", errors.New("the manifest has not been set")
	}

	data := r.remoteCache[remote]
	if data.fetchSchemeURL != "" {
		return data.fetchSchemeURL, nil
	}

	return r.defaultSchemeURL, nil
}

// AddURLRemoteCache is not concurrency safe.
func (r *Repo) AddURLRemoteCache(remote, schemeURL string) error {
	maybeExisting, found := r.remoteCache[remote]
	if found {
		if maybeExisting.fetchSchemeURL == schemeURL {
			// NOP.
			return nil
		}
		if maybeExisting.fetchSchemeURL != "" {
			return fmt.Errorf("remote %q already found. existing: %v attempted: %v", remote, maybeExisting.fetchSchemeURL, schemeURL)
		}
	}
	maybeExisting.fetchSchemeURL = schemeURL
	r.remoteCache[remote] = maybeExisting
	return nil
}

func (r *Repo) Manifest() *manifest.Manifest {
	return r.manifest
}

func (r *Repo) PinnedManifest() *manifest.Manifest {
	return r.pinned
}

func (r *Repo) WorkspaceManifest(root string) (*manifest.Manifest, error) {
	startTime := time.Now()
	pinned, err := manifest.CloneManifest(r.PinnedManifest())
	if err != nil {
		return nil, err
	}

	for _, p := range pinned.Project {
		wsPath := filepath.Join(root, p.GetPath())
		path := filepath.Join(wsPath, bindcas.GitDirName, "HEAD")
		b, err := os.ReadFile(path)
		if err != nil {
			if r.ignoreMissingProjects && entities.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("failed to read workspace HEAD %s: %w", path, err)
		}
		headVal := string(b)

		// repo start is using symbolic refs
		if strings.HasPrefix(headVal, "ref: ") {
			refsPath := strings.TrimPrefix(headVal, "ref: ")
			refsPath = strings.TrimSuffix(refsPath, "\n")
			b, err = os.ReadFile(filepath.Join(wsPath, bindcas.GitDirName, refsPath))
			if err != nil {
				return nil, fmt.Errorf("failed to read symbolic ref %s: %w", filepath.Join(wsPath, bindcas.GitDirName, refsPath), err)
			}
		}
		if len(b) < entities.HashHexSize {
			return nil, fmt.Errorf("invalid hash at path %s", path)
		}
		p.Revision = string(b[0:entities.HashHexSize])
		// Add annoation for current directory hash
		dirHash, err := bindcas.Hash(wsPath)
		if err != nil {
			return nil, fmt.Errorf("cannot get workspace hash for %s: %w", wsPath, err)
		}
		anno := &manifest.Annotation{
			Name:  ABFSDirHashTag,
			Value: dirHash.String(),
		}
		p.Annotation = append(p.Annotation, anno)
	}
	log.Printf("Workspace manifest setup in %f seconds\n", time.Since(startTime).Seconds())
	return pinned, nil
}

func (r *Repo) ManifestFromFile(fileName string) error {
	manifest, err := manifest.ParseFile(fileName)
	if err != nil {
		return err
	}
	return r.setManifest(context.TODO(), manifest)
}

func (r *Repo) ManifestFromBytes(content []byte) error {
	manifest, err := manifest.Parse(content)
	if err != nil {
		return err
	}
	return r.setManifest(context.TODO(), manifest)
}

func (r *Repo) ManifestFromBranch(ctx context.Context, branch, manifestProjectName, manifestBranchFile string) (returnErr error) {
	searchPaths := []string{
		"refs/heads/" + branch,
		"refs/tags/" + branch,
		branch,
	}
	var (
		refHash []entities.ProjectRefHash
		err     error
	)
	for _, ref := range searchPaths {
		refHash, err = r.store.SearchRefs(r.manifestServer, manifestProjectName, ref, 0)
		if err != nil {
			return fmt.Errorf("failed to resolve manifest %s: %w", ref, err)
		}
		if len(refHash) != 0 {
			break
		}
	}
	if len(refHash) == 0 {
		return fmt.Errorf("cannot find manifest for %s", branch)
	}

	h := refHash[0].Hash
	log.Printf("Using the manifest from commit %s for %s", h, branch)

	commit, err := r.git.GetCommit(ctx, h)
	if err != nil {
		return fmt.Errorf("cannot load manifest commit %s: %w", h, err)
	}
	r.rootHash = entities.Hash(commit.TreeHash)
	manifestHash, err := bindcas.ResolveToHash(r.store, r.rootHash, manifestBranchFile)
	if err != nil {
		return fmt.Errorf("cannot get file %q from %s: %s", manifestBranchFile, h, err)
	}
	return r.manifestFromBlobHash(ctx, manifestHash)
}

func (r *Repo) manifestProjectFileBytes(ctx context.Context, name string) ([]byte, error) {
	manifestHash, err := bindcas.ResolveToHash(r.store, r.rootHash, name)
	if err != nil {
		return nil, fmt.Errorf("cannot get file %q from %s: %s", name, r.rootHash, err)
	}

	return store.ReadAllBlob(ctx, r.store, manifestHash)
}

func localManifestFileBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (r *Repo) manifestFromBlobHash(ctx context.Context, manifestHash entities.Hash) error {
	manBytes, err := store.ReadAllBlob(ctx, r.store, manifestHash)
	if err != nil {
		return fmt.Errorf("cannot read default.xml (blob=%s): %s", manifestHash, err)
	}

	manifest, err := manifest.Parse(manBytes)
	if err != nil {
		return err
	}

	return r.setManifest(ctx, manifest)
}

func (r *Repo) ManifestFromBlobHash(ctx context.Context, manifestHash string) error {
	if !plumbing.IsHash(manifestHash) {
		return fmt.Errorf("%q is not a valid hash", manifestHash)
	}
	return r.manifestFromBlobHash(ctx, entities.NewHash(manifestHash))
}

// updateRemoteCache updates remoteCache. It contains a mapping of remoteNames
// to manifest values, which may be derived or extended.
func (r *Repo) updateRemoteCache(man *manifest.Manifest) error {
	manServerSchemeURL := instance.MaybeAddScheme(r.manifestServer, r.manifestScheme)
	manProjectSchemeURL, err := url.JoinPath(manServerSchemeURL, r.manifestProjectName)
	if err != nil {
		return err
	}
	baseSchemeURL, err := url.Parse(manProjectSchemeURL)
	if err != nil {
		return err
	}

	remoteCache := make(map[string]remoteData)
	for _, remote := range man.Remote {
		fetchSchemeURL, err := url.Parse(remote.Fetch)
		if err != nil {
			return err
		}
		// This matches the Python urljoin behavior that the repo tool uses.
		resolvedSchemeURL := baseSchemeURL.ResolveReference(fetchSchemeURL)
		if curr, found := remoteCache[remote.Name]; found {
			if curr.fetchSchemeURL != resolvedSchemeURL.String() {
				return fmt.Errorf("duplicate remote found with different fetch url. Original: %q, Duplicate: %q", curr.fetchSchemeURL, resolvedSchemeURL)
			}
		} else {
			remoteCache[remote.Name] = remoteData{fetchSchemeURL: resolvedSchemeURL.String(), revision: remote.Revision}
		}
	}

	r.remoteCache = remoteCache
	return nil
}

func (r *Repo) setManifest(ctx context.Context, man *manifest.Manifest) error {
	pinned, err := manifest.CloneManifest(man)
	if err != nil {
		return err
	}

	manifestBytes := func(ctx context.Context, name string) ([]byte, error) {
		return r.manifestProjectFileBytes(ctx, name)
	}
	localManifestBytes := func(ctx context.Context, path string) ([]byte, error) {
		return localManifestFileBytes(path)
	}

	if len(pinned.Include) > 0 {
		if r.rootHash == entities.ZeroHash {
			return errors.New("cannot include a file from an unknown manifest project revision. Please mount with `-b` or provide a flat manifest file")
		}
		if err := pinned.FlattenIncludes(ctx, manifestBytes); err != nil {
			return err
		}
	}

	if r.localManifestDir != "" {
		entries, err := os.ReadDir(r.localManifestDir)
		if err != nil {
			return err
		}

		for _, ent := range entries {
			if strings.HasSuffix(ent.Name(), ".xml") {
				path := filepath.Join(r.localManifestDir, ent.Name())
				if err := pinned.IncludeManifestFile(ctx, path, localManifestBytes); err != nil {
					return err
				}
			}
		}
	}

	// Create fast lookup of remote properties.
	err = r.updateRemoteCache(pinned)
	if err != nil {
		return err
	}

	// Resolve the project values that can be inherited.
	for _, p := range pinned.Project {
		if h, ok := r.overrides[p.Name]; ok {
			p.Revision = h.String()
		}
		r.applyInheritedProjectValues(pinned.Default, p)
	}

	if pinned.Default != nil {
		r.defaultSchemeURL = r.remoteCache[pinned.Default.Remote].fetchSchemeURL
	}

	// Get the set of unique (revision, url) that needs resolving.
	// [revision][url]
	toResolve := make(map[string]map[string]struct{})
	for _, p := range pinned.Project {
		if entities.IsHash(p.Revision) {
			continue
		}
		if p.Revision == "" {
			return fmt.Errorf("revision for %s is not specified", p.Name)
		}
		remSchemeURL := r.remoteCache[p.Remote].fetchSchemeURL
		if remSchemeURL == "" {
			remSchemeURL = r.defaultSchemeURL
		}
		m, exists := toResolve[p.Revision]
		if !exists {
			m = make(map[string]struct{})
			toResolve[p.Revision] = m
		}
		m[remSchemeURL] = struct{}{}
	}

	// Issue requests to resolve.
	refs := make(projectMap)
	for rev := range toResolve {
		for url := range toResolve[rev] {
			for _, ref := range resolutionOrder(rev) {
				if err := r.updateRefs(url, ref, refs); err != nil {
					return err
				}
			}
		}
	}

	// Assign the resolved revision hash to the projects.
	for _, p := range pinned.Project {
		if entities.IsHash(p.Revision) {
			continue
		}
		found := false
		remURL := r.remoteCache[p.Remote].fetchSchemeURL
		if remURL == "" {
			remURL = r.defaultSchemeURL
		}

		for _, ref := range resolutionOrder(p.Revision) {
			if h, ok := refs[p.Name][ref][remURL]; ok {
				p.Revision = h.String()
				found = true
				break
			}
		}
		if !found {
			// Scheme will show up here in the error, but this is at the manifest parsing layer, and scheme is needed here to create or edit manifests.
			return fmt.Errorf("cannot resolve %q for project %q at URL %q", p.Revision, p.Name, remURL)
		}
	}

	r.manifest = man
	r.pinned = pinned
	r.manifestSet = true
	return nil
}

func (r *Repo) applyInheritedProjectValues(def *manifest.Default, project *manifest.Project) {
	if project.Remote == "" {
		if def != nil {
			project.Remote = def.Remote
		}
	}

	if project.Revision == "" {
		// Revision can come from the remote with higher priority than default.
		if rev, ok := r.remoteCache[project.Remote]; ok {
			project.Revision = rev.revision
		}
		if project.Revision == "" {
			if def != nil {
				project.Revision = def.Revision
			}
		}
	}

	if def == nil {
		return
	}

	if project.DestBranch == "" {
		project.DestBranch = def.DestBranch
	}
	if project.Upstream == "" {
		project.Upstream = def.Upstream
	}
	if project.SyncC == "" {
		project.SyncC = def.SyncC
	}
	if project.SyncS == "" {
		project.SyncS = def.SyncS
	}
}

func resolutionOrder(rev string) []string {
	if strings.HasPrefix(rev, "refs/") {
		return []string{rev}
	}
	return []string{rev, "refs/tags/" + rev, "refs/heads/" + rev}
}

func (r *Repo) updateRefs(url, ref string, refs projectMap) error {
	results, err := r.store.SearchRefs(url, "", ref, 0)
	if err != nil {
		return fmt.Errorf("failed to SearchRefs for url: %q and ref: %q err: %w", url, ref, err)
	}
	for _, r := range results {
		m, exists := refs[r.Project]
		if !exists {
			m = make(projectRefMap)
			refs[r.Project] = m
		}
		m2, exists := m[r.Ref]
		if !exists {
			m2 = make(projectRefURLMap)
			m[r.Ref] = m2
		}
		m2[url] = r.Hash
	}
	return nil
}

func (r *Repo) AddChanges(changeIDs []string) error {
	for _, id := range changeIDs {
		ref := fmt.Sprintf("refs/changes/%s/%s", id[len(id)-2:], id)
		refHash, err := r.store.SearchRefs(r.manifestServer, "", ref, 0)
		if err != nil {
			return fmt.Errorf("cannot find ref %s: %w", ref, err)
		}

		if len(refHash) == 0 {
			return fmt.Errorf("cannot find ref %s", ref)
		}

		slices.SortFunc(refHash, func(a, b entities.ProjectRefHash) int {
			ai, _ := strconv.Atoi(a.Ref)
			bi, _ := strconv.Atoi(b.Ref)
			return ai - bi
		})

		hash := refHash[0].Hash
		project := refHash[0].Project
		r.overrides[project] = hash
	}
	return nil
}
