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
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/filesystem/dirsync"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

const wsFile = ".abfs.ws"

type ActiveWS struct {
	workspaces *Workspaces

	Name      string
	Path      string
	casfs     bindcas.CASFS
	mntDaemon binding.MountDaemon
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	complexSync bool

	// statusLock protects changes to the following fields
	statusLock    sync.Mutex
	lastSyncStart time.Time
	lastSyncEnd   time.Time
	lastSyncErr   error
	lastRemotePut time.Time
	lastLocal     *binding.WSSnapshot
	lastRemote    *binding.WSSnapshot
}

func NewActive(wsName, path string, wsSnap *binding.WSSnapshot, ws *Workspaces, mntDaemon binding.MountDaemon, casfs bindcas.CASFS) *ActiveWS {
	return &ActiveWS{
		workspaces: ws,
		Path:       path,
		Name:       wsName,
		mntDaemon:  mntDaemon,
		casfs:      casfs,
		lastLocal:  wsSnap,
	}
}

func (m *ActiveWS) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	m.RemoveStatusFile()
}

func (m *ActiveWS) GetStatus() *binding.WorkspaceStatus {
	m.statusLock.Lock()
	defer m.statusLock.Unlock()

	var lastSyncErr string
	if m.lastSyncErr != nil {
		lastSyncErr = m.lastSyncErr.Error()
	}
	return &binding.WorkspaceStatus{
		Name:          m.Name,
		Path:          m.Path,
		Conflicts:     slices.Clone(m.lastLocal.Snapshot.Conflicts),
		LastSyncError: lastSyncErr,
		LastSyncStart: m.lastSyncStart,
		LastSyncEnd:   m.lastSyncEnd,
		LastSnapshot:  m.lastLocal.Hash,
		LastUpdate:    m.lastRemotePut,
	}
}

func (m *ActiveWS) PutWSSnapshot(ctx context.Context, wsSnap *binding.WSSnapshot, prevHash entities.Hash) error {
	if err := m.workspaces.PutWSSnapshot(ctx, m.Name, wsSnap, prevHash); err != nil {
		return err
	}
	m.lastLocal = wsSnap
	m.lastRemote = wsSnap
	m.lastRemotePut = time.Now()
	return nil
}

func ReadStatus(root string) (binding.WorkspaceStatus, error) {
	filePath := path.Join(root, wsFile)
	b, err := os.ReadFile(filePath)
	if err != nil {
		return binding.WorkspaceStatus{}, err
	}
	var status binding.WorkspaceStatus
	if err := json.Unmarshal(b, &status); err != nil {
		log.Printf("Could not unmarshal workspace status: %v", err)
		return binding.WorkspaceStatus{}, fs.ErrNotExist
	}
	return status, nil
}

func (m *ActiveWS) WriteStatus() error {
	filePath := path.Join(m.Path, wsFile)
	status := m.GetStatus()
	b, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filePath, b, 0644); err != nil {
		return err
	}
	return m.casfs.SetExcluded(filePath, true)
}

func (m *ActiveWS) Sync(ctx context.Context) {
	m.wg.Add(1)
	defer m.wg.Done()

	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}

		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Println("Recovered from panic in doSync:", r)
					debug.PrintStack()
				}
			}()
			err := m.doSync(ctx)
			if err != nil {
				log.Println("Sync failed:", err)
			}
		}()

	}
}

func (m *ActiveWS) RemoveStatusFile() {
	filePath := path.Join(m.Path, wsFile)
	_ = os.Remove(filePath)
}

type indexDiff struct {
	WorkDir string
	GitDir  string
	OldHash binding.Hash
	NewHash binding.Hash
}

type SnapDiff struct {
	dirDiffs   map[string][]dirsync.DiffAction
	indexDiffs map[string]indexDiff
}

func (d SnapDiff) Count() int64 {
	count := int64(len(d.indexDiffs))
	for _, actions := range d.dirDiffs {
		count += int64(len(actions))
	}
	return count
}

func (m *ActiveWS) diffFromSnapshot(ctx context.Context, oldSnap, curSnap binding.Snapshot) (SnapDiff, error) {
	snapDiffs := SnapDiff{
		dirDiffs:   make(map[string][]dirsync.DiffAction),
		indexDiffs: make(map[string]indexDiff),
	}

	grp, ctx := errgroup.WithContext(ctx)
	var lock sync.Mutex
	diff := func(dir string, oldHash, curHash entities.Hash) error {
		if oldHash.IsZero() {
			oldHash = entities.EmptyDirHash
		}
		if curHash.IsZero() {
			curHash = entities.EmptyDirHash
		}
		diff, err := dirsync.DirDiffList(ctx, m.workspaces.Store, oldHash, curHash, true)
		if err != nil {
			return fmt.Errorf("failed to calculate diff for dir %s (%s -> %s): %w", dir, oldHash, curHash, err)
		}
		if len(diff) > 0 {
			lock.Lock()
			snapDiffs.dirDiffs[dir] = diff
			lock.Unlock()
		}
		return nil
	}

	grp.SetLimit(runtime.NumCPU())
	grp.Go(func() error {
		return diff(".", oldSnap.Root, curSnap.Root)
	})

	oldRepos := make(map[string]binding.GitRepoSnapshot)
	for _, repo := range oldSnap.GitRepos {
		oldRepos[repo.WorkDir] = repo
	}

	curRepos := make(map[string]binding.GitRepoSnapshot)
	for _, repo := range curSnap.GitRepos {
		curRepos[repo.WorkDir] = repo
	}

	workDirs := make(map[string]bool)
	for wd := range oldRepos {
		workDirs[wd] = true
	}
	for wd := range curRepos {
		workDirs[wd] = true
	}

	diffRepo := func(oldRepo, curRepo binding.GitRepoSnapshot) error {
		workDir := curRepo.WorkDir
		if workDir == "" {
			workDir = oldRepo.WorkDir
		}
		gitDir := curRepo.GitDir
		if gitDir == "" {
			gitDir = oldRepo.GitDir
		}
		objectsDir := curRepo.ObjectsDir
		if objectsDir == "" {
			objectsDir = oldRepo.ObjectsDir
		}

		gitDir = strings.Replace(gitDir, "{workdir}", workDir, 1)
		objectsDir = strings.Replace(objectsDir, "{workdir}", workDir, 1)

		if err := diff(workDir, oldRepo.WorkDirHash, curRepo.WorkDirHash); err != nil {
			return err
		}

		if err := diff(gitDir, oldRepo.GitDirHash, curRepo.GitDirHash); err != nil {
			return err
		}

		if err := diff(path.Join(objectsDir, "pack"), oldRepo.PacksHash, curRepo.PacksHash); err != nil {
			return err
		}

		if oldRepo.IndexHash != curRepo.IndexHash || oldRepo.GitDirHash != curRepo.GitDirHash {
			lock.Lock()
			snapDiffs.indexDiffs[workDir] = indexDiff{
				WorkDir: workDir,
				GitDir:  gitDir,
				OldHash: oldRepo.IndexHash,
				NewHash: curRepo.IndexHash,
			}
			lock.Unlock()
		}

		return nil
	}

	for wd := range workDirs {
		grp.Go(func() error {
			defer func() {
				if r := recover(); r != nil {
					log.Println("Recovered from panic in diffRepo:", r)
					debug.PrintStack()
				}
			}()
			return diffRepo(oldRepos[wd], curRepos[wd])
		})
	}

	if err := grp.Wait(); err != nil {
		return snapDiffs, err
	}
	return snapDiffs, nil
}

func (m *ActiveWS) fetchRemote(ctx context.Context) (*binding.WSSnapshot, error) {
	remoteHash, err := m.workspaces.SnapshotHash(m.Name)
	if err != nil {
		return nil, err
	}
	if m.lastRemote != nil && m.lastRemote.Hash == remoteHash {
		return m.lastRemote, nil
	}
	return m.workspaces.FetchSnapshotByHash(ctx, remoteHash)
}

func (m *ActiveWS) applyDiff(ctx context.Context, snapDiff SnapDiff) error {
	grp, ctx := errgroup.WithContext(ctx)
	grp.SetLimit(runtime.NumCPU())
	for dir, diff := range snapDiff.dirDiffs {
		grp.Go(func() error {
			return dirsync.DirSyncDiff(ctx, m.workspaces.Store, path.Join(m.Path, dir), diff)
		})
	}
	return grp.Wait()
}

func (m *ActiveWS) excludeConflicts(diff SnapDiff, conflicts []string) map[string]dirsync.DiffAction {
	conflictMap := make(map[string]struct{})
	for _, conflict := range conflicts {
		conflictMap[conflict] = struct{}{}
	}
	result := make(map[string]dirsync.DiffAction)
	for dir, actions := range diff.dirDiffs {
		for _, action := range actions {
			p := path.Join(dir, action.Path)
			if _, ok := conflictMap[p]; !ok {
				result[p] = action
			}
		}
	}
	return result
}

func (m *ActiveWS) doSync(ctx context.Context) (retErr error) {
	m.statusLock.Lock()
	m.lastSyncStart = time.Now()
	m.statusLock.Unlock()

	defer func() {
		m.statusLock.Lock()
		m.lastSyncEnd = time.Now()
		m.lastSyncErr = retErr
		m.statusLock.Unlock()

		if err := m.WriteStatus(); err != nil {
			retErr = err
		}
	}()

	curSnap, err := m.mntDaemon.Snapshot(ctx)
	if err != nil {
		return err
	}
	localChanged := !curSnap.Equals(m.lastLocal.Snapshot)

	curRemote, err := m.fetchRemote(ctx)
	if err != nil {
		return err
	}
	remoteChanged := m.lastRemote == nil || curRemote.Hash != m.lastRemote.Hash

	if !remoteChanged {
		if !localChanged {
			m.lastRemote = curRemote
			return nil
		}

		wsSnap := &binding.WSSnapshot{
			Version:      binding.CurrentWSSnapshotVersion,
			MountDetails: m.mntDaemon.GetMountDetails().Hash,
			Time:         time.Now(),
			Snapshot:     curSnap,
		}
		return m.PutWSSnapshot(ctx, wsSnap, curRemote.Hash)
	}

	if !localChanged && len(curSnap.Conflicts) == 0 && len(curRemote.Snapshot.Conflicts) == 0 {
		if err := m.mntDaemon.Restore(ctx, curRemote.Snapshot); err != nil {
			return err
		}
		m.statusLock.Lock()
		m.lastLocal = curRemote
		m.lastRemote = curRemote
		m.statusLock.Unlock()
		return nil
	}

	if !m.complexSync {
		return fmt.Errorf("both local and remote have changed and it requires a complex sync process")
	}

	localDiff, err := m.diffFromSnapshot(ctx, m.lastLocal.Snapshot, curSnap)
	if err != nil {
		return fmt.Errorf("failed to calculate local diff from the previous snapshot (%s): %w", m.lastLocal.Hash, err)
	}
	localMap := m.excludeConflicts(localDiff, curSnap.Conflicts)

	var lastRemoteHash entities.Hash
	var lastRemoteSnap binding.Snapshot
	if m.lastRemote != nil {
		lastRemoteSnap = m.lastRemote.Snapshot
		lastRemoteHash = m.lastRemote.Hash
	}
	remoteDiff, err := m.diffFromSnapshot(ctx, lastRemoteSnap, curRemote.Snapshot)
	if err != nil {
		return fmt.Errorf("failed to calculate remote diff from the previous snapshot (%s) to current (%s): %w", lastRemoteHash, curRemote.Hash, err)
	}
	remoteMap := m.excludeConflicts(remoteDiff, curRemote.Snapshot.Conflicts)

	newConflicts := m.newConflicts(localMap, remoteMap)
	conflicts := append(m.lastLocal.Snapshot.Conflicts, curSnap.Conflicts...)

	if len(localDiff.indexDiffs) == 0 && len(localMap) == 0 && len(remoteDiff.indexDiffs) == 0 && len(remoteMap) == 0 && len(newConflicts) == 0 {
		return nil
	}

	if len(conflicts) > 0 {
		localMap = m.excludeConflicts(localDiff, conflicts)
	}

	for _, p := range m.lastRemote.Snapshot.GitRepos {
		if _, ok := m.mntDaemon.GetGitRepo(p.WorkDir); !ok {
			gitDir := strings.Replace(p.GitDir, "{workdir}", p.WorkDir, 1)
			objectsDir := strings.Replace(p.ObjectsDir, "{workdir}", p.WorkDir, 1)
			if err := SetupEmptyGitRepos(m.casfs, m.Path, p.WorkDir, gitDir, objectsDir); err != nil {
				return fmt.Errorf("failed to setup empty git repo for %s: %w", p.WorkDir, err)
			}
		}
	}

	if err := m.applyDiff(ctx, remoteDiff); err != nil {
		return fmt.Errorf("failed to apply remote diff: %w", err)
	}

	newSnap, err := m.mntDaemon.Snapshot(ctx)
	if err != nil {
		return err
	}
	newSnap.Conflicts = conflicts
	wsSnap := &binding.WSSnapshot{
		Version:      binding.CurrentWSSnapshotVersion,
		MountDetails: m.mntDaemon.GetMountDetails().Hash,
		Time:         time.Now(),
		Snapshot:     newSnap,
	}
	if len(localMap) > 0 || len(localDiff.indexDiffs) > 0 || len(newConflicts) > 0 {
		if err := m.PutWSSnapshot(ctx, wsSnap, m.lastRemote.Hash); err != nil {
			return fmt.Errorf("failed to put new snapshot: %w", err)
		}
	}

	return nil
}

func (m *ActiveWS) newConflicts(localMap map[string]dirsync.DiffAction, remoteMap map[string]dirsync.DiffAction) []string {
	var conflicts []string
	for p, l := range localMap {
		if r, ok := remoteMap[p]; ok {
			if l.HashB != r.HashB || l.ModeB != r.ModeB {
				conflicts = append(conflicts, p)
			}
		}
	}
	return conflicts
}

func SetupEmptyGitRepos(casfs bindcas.CASFS, root, workDir, gitDir, objectsDir string) error {
	workDirAbs := filepath.Join(root, workDir)
	gitDirAbs := filepath.Join(root, gitDir)
	objectsDirAbs := filepath.Join(root, objectsDir)
	if err := os.MkdirAll(workDirAbs, 0755); err != nil {
		return err
	}
	if err := casfs.SetExcluded(workDirAbs, true); err != nil {
		return err
	}

	if err := os.MkdirAll(gitDirAbs, 0755); err != nil {
		return err
	}
	if err := casfs.SetExcluded(gitDirAbs, true); err != nil {
		return err
	}
	workDirGit := filepath.Join(workDir, ".git")
	if gitDir != workDirGit {
		rel, err := filepath.Rel(workDir, gitDir)
		if err != nil {
			return err
		}
		if err := os.Symlink(rel, workDirGit); err != nil {
			return err
		}
		if err := casfs.SetExcluded(workDirGit, true); err != nil {
			return err
		}
	}

	if err := casfs.CreateGitObjects(path.Dir(objectsDirAbs), path.Base(objectsDir)); err != nil {
		return err
	}
	if err := os.MkdirAll(path.Join(objectsDirAbs, "pack"), 0755); err != nil {
		return err
	}

	gitDirObjects := path.Join(gitDir, "objects")
	gitDirObjectsAbs := path.Join(root, gitDirObjects)
	if objectsDir != gitDirObjects {
		rel, err := filepath.Rel(gitDir, objectsDir)
		if err != nil {
			return err
		}
		if err := os.Symlink(rel, gitDirObjectsAbs); err != nil {
			return err
		}
		if err := casfs.SetExcluded(gitDirObjectsAbs, true); err != nil {
			return err
		}
	}

	return nil
}
