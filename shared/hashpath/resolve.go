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

package hashpath

import (
	"bytes"
	"context"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
)

type Resolution struct {
	// FS is a CAS in-memory file-system rooted at the base object. It will be nil if the base is pointing to a blob or the hash-path was simply a path.
	FS bindcas.MemFS
	// Ref is a fully-qualified reference if a ref is used, otherwise is an empty string.
	Ref string
	// Base is the hash of resolved object either specified explicitly or resolved from the ref.
	Base entities.Hash
	// DirHash is the hash of a tree (directory) object resolved from Base (like if Base is a commit, DirHash is its tree-hash).
	DirHash entities.Hash
	// Resolved is the hash of the target object, found by resolving the base and starting from the directory specified by DirHash.
	Resolved entities.Hash
}

type BaseDirRef struct {
	Base entities.Hash
	Dir  entities.Hash
	Ref  string
}

func ResolveBase(ctx context.Context, store entities.CASStore, hp *HashPath, defaultServer, defaultProject string) (BaseDirRef, error) {
	base, ref, err := baseHash(store, hp, defaultServer, defaultProject)
	if err != nil {
		return BaseDirRef{}, err
	}

	dirHash, err := resolveToDir(ctx, store, base)
	if err != nil {
		return BaseDirRef{}, err
	}

	return BaseDirRef{Base: base, Dir: dirHash, Ref: ref}, nil
}

func ResolvePath(ctx context.Context, store entities.CASStore, baseDirRef BaseDirRef, hp *HashPath) (*Resolution, error) {
	entHash, err := bindcas.ResolveToHash(store, baseDirRef.Dir, hp.Path)
	if err != nil {
		return nil, err
	}

	fs, err := bindcas.NewMemFS(store, baseDirRef.Dir)
	if err != nil {
		return nil, err
	}

	return &Resolution{
		Base:     baseDirRef.Base,
		DirHash:  baseDirRef.Dir,
		Resolved: entHash,
		Ref:      baseDirRef.Ref,
		FS:       fs,
	}, nil
}

// Resolve is split into two methods:
// ResolveBase -> Uses remote Ref APIs to lookup base and dir hashes.
// ResolvePath -> Can navigate from the dir hash to the path of interest.
// They are split here to allow independent caching.
func Resolve(ctx context.Context, store entities.CASStore, hp *HashPath, defaultServer, defaultProject string) (*Resolution, error) {
	if hp.IsSimpleHash() {
		return &Resolution{
			Base:     hp.Hash,
			Resolved: hp.Hash,
		}, nil
	}

	baseDirRef, err := ResolveBase(ctx, store, hp, defaultServer, defaultProject)
	if err != nil {
		return nil, err
	}

	return ResolvePath(ctx, store, baseDirRef, hp)
}

func resolveToDir(ctx context.Context, store entities.CASStore, hash entities.Hash) (entities.Hash, error) {
	for {
		content, err := store.GetObject(ctx, hash)
		if err != nil {
			return entities.Hash{}, err
		}

		t, _, err := objects.ReadHeader(bytes.NewReader(content))
		if err != nil {
			return entities.Hash{}, err
		}

		switch t {
		case "tag":
			tag, err := gitobjects.DecodeTag(plumbing.Hash(hash), content)
			if err != nil {
				return entities.Hash{}, err
			}
			hash = entities.Hash(tag.Hash)
		case "commit":
			commit, err := gitobjects.DecodeCommit(plumbing.Hash(hash), content)
			if err != nil {
				return entities.Hash{}, err
			}
			return entities.Hash(commit.TreeHash), nil
		case "tree":
			return hash, nil
		default:
			return entities.Hash{}, fmt.Errorf("cannot resolve %s with type %s", hash, t)
		}
	}
}

func baseHash(store entities.CASStore, hp *HashPath, defaultServer, defaultProject string) (entities.Hash, string, error) {
	if !hp.Hash.IsZero() {
		return hp.Hash, "", nil
	}

	server := hp.Server
	if server == "" {
		if defaultServer == "" {
			return entities.Hash{}, "", fmt.Errorf("no server")
		}
		server = defaultServer
	}

	project := hp.Project
	if project == "" {
		if defaultProject == "" {
			return entities.Hash{}, "", fmt.Errorf("no project")
		}
		project = defaultProject
	}
	return resolveRef(store, hp.Ref, server, project)
}

func resolveRef(store entities.CASStore, ref, server, project string) (entities.Hash, string, error) {
	lookupOrder := []string{ref, "refs/heads/" + ref, "refs/tags/" + ref, "refs/remotes/" + ref}
	hashes, err := store.GetRefs(server, project, lookupOrder)
	if err != nil {
		return entities.Hash{}, "", nil
	}

	for i, h := range hashes {
		if !h.IsZero() {
			return h, fmt.Sprintf("%s@%s@%s", lookupOrder[i], project, server), nil
		}
	}
	return entities.Hash{}, "", entities.NotExistErrf("cannot find reference %s of %s@%s", ref, project, server)
}
