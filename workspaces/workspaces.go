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

package workspaces

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/user"
)

type Workspaces struct {
	User     string
	Store    entities.CASStore
	Cacheman *client.Client
}

func userName() string {
	currUser, err := user.CurrentUser()
	if err != nil {
		panic(fmt.Errorf("failed to get the current user: %w", err))
	}
	return currUser.Username
}

func NewWorkspaces(store entities.CASStore, cacheman *client.Client) *Workspaces {
	return &Workspaces{
		User:     userName(),
		Store:    store,
		Cacheman: cacheman,
	}
}

func (s *Workspaces) StatusFromPath(path string) (binding.WorkspaceStatus, error) {
	root, err := binding.FindRootForSocket(path, entities.ABFSSocketFileName)
	if err != nil {
		return binding.WorkspaceStatus{}, err
	}

	return ReadStatus(root)
}

func (s *Workspaces) wsRef(wsName string) string {
	return fmt.Sprintf("refs/abfs/ws/user/%s/%s", s.User, wsName)
}

func (s *Workspaces) List() (map[string]entities.Hash, error) {
	prefix := s.wsRef("")
	refs, err := s.Store.SearchRefs(entities.ABFSMetaServer, entities.ABFSMetaProject, prefix, 0)
	if err != nil {
		return nil, err
	}

	result := make(map[string]entities.Hash, len(refs))
	for _, r := range refs {
		wsName := strings.TrimPrefix(r.Ref, prefix)
		result[wsName] = r.Hash
	}
	return result, nil
}

func (s *Workspaces) SnapshotHash(wsName string) (entities.Hash, error) {
	ref := s.wsRef(wsName)
	refs, err := s.Store.GetRefs(entities.ABFSMetaServer, entities.ABFSMetaProject, []string{ref})
	if err != nil {
		return entities.Hash{}, fmt.Errorf("failed to get workspace %s: %w", wsName, err)
	}
	if len(refs) == 0 || refs[0].IsZero() {
		return entities.Hash{}, entities.NotExistErrf("workspace %s does not exist", wsName)
	}
	return refs[0], nil
}

func (s *Workspaces) PutSnapshotHash(wsName string, hash, prevHash entities.Hash) error {
	if !hash.IsZero() {
		if err := s.Cacheman.WaitFor(0, 1.0, []entities.Hash{hash}); err != nil {
			return err
		}
	}

	if !prevHash.IsZero() {
		refs, err := s.Cacheman.GetRefs(entities.ABFSMetaServer, entities.ABFSMetaProject, []string{s.wsRef(wsName)})
		if err != nil {
			return err
		}
		if refs[0] != prevHash {
			return fmt.Errorf("cannot update workspace %s to %s, because it has been updated to %s while processing", wsName, hash, refs[0])
		}
	}
	return s.Cacheman.PutRefs(entities.ABFSMetaServer, entities.ABFSMetaProject, []entities.RefHash{
		{
			Ref:  s.wsRef(wsName),
			Hash: hash,
		},
	}, true)
}

func (s *Workspaces) PutWSSnapshot(ctx context.Context, wsName string, wsSnap *binding.WSSnapshot, prevHash entities.Hash) error {
	if err := wsSnap.Encode(); err != nil {
		return err
	}

	if err := s.Store.PutObject(ctx, wsSnap.Hash, wsSnap.Encoded); err != nil {
		return err
	}

	if err := s.PutSnapshotHash(wsName, wsSnap.Hash, prevHash); err != nil {
		return err
	}

	return nil
}

func (s *Workspaces) FetchWSSnapshot(ctx context.Context, wsName string) (*binding.WSSnapshot, error) {
	hash, err := s.SnapshotHash(wsName)
	if err != nil {
		return nil, err
	}
	return s.FetchSnapshotByHash(ctx, hash)
}

func (s *Workspaces) FetchSnapshotByHash(ctx context.Context, hash entities.Hash) (*binding.WSSnapshot, error) {
	return binding.FetchObject[binding.WSSnapshot](ctx, s.Store, hash)
}

func (s *Workspaces) Remove(wsName string, prevHash entities.Hash) error {
	return s.PutSnapshotHash(wsName, entities.ZeroHash, prevHash)
}

func (s *Workspaces) Resolve(paths []string) error {
	panic("unimplemented")
}

func (s *Workspaces) Clone(name string, newWS string) error {
	hash, err := s.SnapshotHash(name)
	if err != nil {
		if entities.IsNotExist(err) {
			return clierr.CLIErr("Workspace %s does not exist.", name)
		}
		return err
	}

	if _, err := s.SnapshotHash(newWS); err != nil {
		if !entities.IsNotExist(err) {
			return err
		}
	} else {
		return clierr.CLIErr("Workspace %s already exists.", newWS)
	}

	return s.PutSnapshotHash(newWS, hash, entities.ZeroHash)
}
