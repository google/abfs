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

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/workspaces"
)

func (c *casfs) putWorkspace(wsName string, isNew bool, strategy binding.PutStrategy) error {
	c.wsLock.Lock()
	defer c.wsLock.Unlock()

	ctx := context.Background()

	if c.activeWS != nil {
		return errors.New("an active workspace already exists")
	}

	wsHash, err := c.workspaces.SnapshotHash(wsName)
	if err != nil {
		if !entities.IsNotExist(err) {
			return err
		}
	}
	var wsSnap *binding.WSSnapshot
	if !wsHash.IsZero() {
		wsSnap, err = binding.FetchObject[binding.WSSnapshot](ctx, c.store, wsHash)
		if err != nil {
			return fmt.Errorf("failed to get workspace snapshot: %w", err)
		}
	}

	if isNew && wsSnap != nil {
		return fmt.Errorf("workspace %s already exists", wsName)
	}
	if !isNew && wsSnap == nil {
		return fmt.Errorf("workspace %s does not exist", wsName)
	}

	var finalSnap binding.Snapshot
	if isNew || strategy != binding.PutStrategyRemoteWin {
		currSnap, err := c.snapshotService.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("failed to get workspace snapshot: %w", err)
		}
		if !isNew && strategy == binding.PutStrategyExactMatch {
			if !currSnap.Equals(wsSnap.Snapshot) {
				return fmt.Errorf("existing workdir state does not match specified workspace %s", wsName)
			}
		}
		finalSnap = currSnap
	} else {
		// RemoteWin
		if err := c.snapshotService.Restore(ctx, wsSnap.Snapshot); err != nil {
			return err
		}
		finalSnap = wsSnap.Snapshot
	}

	newSnap := &binding.WSSnapshot{
		Version:      binding.CurrentWSSnapshotVersion,
		MountDetails: c.mountDetails.Hash,
		Time:         time.Now(),
		Snapshot:     finalSnap,
	}

	active := workspaces.NewActive(wsName, c.mountPoint, newSnap, c.workspaces, c.snapshotService, c.mnt.CASFS())

	var prevHash entities.Hash
	if wsSnap != nil {
		prevHash = wsSnap.Hash
	}
	if err := active.PutWSSnapshot(ctx, newSnap, prevHash); err != nil {
		return err
	}
	if err := active.WriteStatus(); err != nil {
		return err
	}

	c.activeWS = active

	go active.Sync(ctx)

	return nil
}

func (c *casfs) getWorkspaceStatus() (*binding.WorkspaceStatus, error) {
	c.wsLock.Lock()
	defer c.wsLock.Unlock()

	if c.activeWS == nil {
		return nil, nil
	}

	status := c.activeWS.GetStatus()
	if status == nil {
		return nil, nil
	}

	return status, nil
}

func (c *casfs) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	status, err := c.getWorkspaceStatus()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(status)
}

func (c *casfs) handlePostWorkspace(w http.ResponseWriter, r *http.Request) {
	var req binding.PutWSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !req.Strategy.IsValid() {
		http.Error(w, fmt.Sprintf("invalid strategy %q", req.Strategy), http.StatusBadRequest)
		return
	}
	if err := c.putWorkspace(req.WS, req.IsNew, req.Strategy); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (c *casfs) removeWorkspace() error {
	c.wsLock.Lock()
	defer c.wsLock.Unlock()

	if c.activeWS == nil {
		return nil
	}

	c.activeWS.Stop()
	c.activeWS = nil

	return nil
}

func (c *casfs) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	err := c.removeWorkspace()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
