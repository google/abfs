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
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/abfsrepo"
	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/filesystem/dirsync"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/workspaces"
)

func (c *casfs) handlePutGitRepo(w http.ResponseWriter, r *http.Request) {
	workTree := mux.Vars(r)["workdir"]
	workTree = path.Clean(workTree)
	workTree = strings.Trim(workTree, string(os.PathSeparator))

	var repo binding.GitRepo
	if err := json.NewDecoder(r.Body).Decode(&repo); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	c.snapshotService.AddGitRepo(workTree, repo)
}

func (c *casfs) handleGetSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, err := c.snapshotService.Snapshot(r.Context())
	if err != nil {
		http.Error(w, "failed to generate the snapshot: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := json.NewEncoder(w).Encode(snapshot); err != nil {
		log.Printf("failed to encode snapshot response: %v", err)
	}
}

func (c *casfs) handlePutSnapshot(w http.ResponseWriter, r *http.Request) {
	var snapshot binding.Snapshot
	if err := json.NewDecoder(r.Body).Decode(&snapshot); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := c.snapshotService.Restore(r.Context(), snapshot); err != nil {
		http.Error(w, "failed to restore the snapshot: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

type indexSnapCache struct {
	indexMTime   time.Time
	headTreeHash entities.Hash
	snapHash     entities.Hash
	entries      int
}

type gitRepo struct {
	workDir          string
	gitDir           string
	objectsDir       string
	cachedGitDirHash entities.Hash
	cachedHeadTree   entities.Hash
	indexSnap        indexSnapCache
}

func (g *gitRepo) IndexPath(root string) string {
	return filepath.Join(root, g.gitDir, "index")
}

type SnapshotService struct {
	casfs        bindcas.CASFS
	store        entities.CASStore
	cacheman     *client.Client
	git          *casgit.CASGit
	mountPoint   string
	mountDetails *binding.MountDetails
	lock         sync.Mutex
	reposLock    sync.Mutex
	gitRepos     map[string]*gitRepo
}

func NewSnapshotService(casfs *casfs) *SnapshotService {
	return &SnapshotService{
		casfs:        casfs.mnt.CASFS(),
		store:        casfs.store,
		cacheman:     casfs.cacheman,
		git:          casgit.New(casfs.store),
		mountPoint:   casfs.mountPoint,
		mountDetails: casfs.mountDetails,
		gitRepos:     make(map[string]*gitRepo),
	}
}

func (c *SnapshotService) AddGitRepo(workDir string, repo binding.GitRepo) {
	c.lock.Lock()
	defer c.lock.Unlock()

	r := &gitRepo{
		workDir:    workDir,
		gitDir:     repo.GitDir,
		objectsDir: repo.ObjectsDir,
		indexSnap: indexSnapCache{
			indexMTime:   time.Now().Add(1 * time.Second),
			headTreeHash: repo.HeadTreeHash,
			snapHash:     binding.EmptyIndexSnapObject.Hash,
			entries:      0,
		},
	}

	c.reposLock.Lock()
	defer c.reposLock.Unlock()
	//TODO what if duplicate?
	c.gitRepos[workDir] = r
}

func (c *SnapshotService) GetGitRepo(workDir string) (binding.GitRepo, bool) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.reposLock.Lock()
	defer c.reposLock.Unlock()

	repo, ok := c.gitRepos[workDir]
	if !ok {
		return binding.GitRepo{}, false
	}
	return binding.GitRepo{
		GitDir:     repo.gitDir,
		ObjectsDir: repo.objectsDir,
	}, true
}

func (c *SnapshotService) GetMountDetails() *binding.MountDetails {
	return c.mountDetails
}

func (c *SnapshotService) Snapshot(ctx context.Context) (binding.Snapshot, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.snapshot(ctx)
}

func (c *SnapshotService) Restore(ctx context.Context, snapshot binding.Snapshot) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.restore(ctx, snapshot)
}

func (c *SnapshotService) snapshot(ctx context.Context) (binding.Snapshot, error) {
	var snapshot binding.Snapshot

	var lock sync.Mutex
	grp, _ := errgroup.WithContext(ctx)
	grp.SetLimit(runtime.NumCPU())
	grp.Go(func() error {
		root, err := c.casfs.Hash(c.mountPoint)
		if err != nil {
			return err
		}
		snapshot.Root = root
		return nil
	})
	for _, repo := range c.gitRepos {
		grp.Go(func() error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			repoSnap, err := c.snapshotGitRepo(ctx, repo)
			if err != nil {
				return err
			}
			lock.Lock()
			defer lock.Unlock()
			snapshot.GitRepos = append(snapshot.GitRepos, repoSnap)
			return nil
		})
	}
	if err := grp.Wait(); err != nil {
		return binding.Snapshot{}, err
	}

	sort.Slice(snapshot.GitRepos, func(i, j int) bool {
		return snapshot.GitRepos[i].WorkDir < snapshot.GitRepos[j].WorkDir
	})

	return snapshot, nil
}

func (c *SnapshotService) snapshotGitRepo(ctx context.Context, gitRepo *gitRepo) (binding.GitRepoSnapshot, error) {
	workDirHash, err := c.casfs.Hash(gitRepo.workDir)
	if err != nil {
		return binding.GitRepoSnapshot{}, err
	}

	packs, err := c.casfs.Hash(path.Join(c.mountPoint, gitRepo.objectsDir, "/pack"))
	if err != nil {
		return binding.GitRepoSnapshot{}, err
	}

	indexPath := gitRepo.IndexPath(c.mountPoint)

	// Ensure index is excluded (git can rewrite the file which clears the exclusion flag)
	if isExcluded, err := bindcas.Excluded(indexPath); err != nil {
		return binding.GitRepoSnapshot{}, err
	} else if !isExcluded {
		if err := bindcas.SetExcluded(indexPath, true); err != nil {
			return binding.GitRepoSnapshot{}, err
		}
	}
	gitDirHash, err := c.casfs.Hash(gitRepo.gitDir)
	if err != nil {
		return binding.GitRepoSnapshot{}, err
	}
	if gitDirHash != gitRepo.cachedGitDirHash {
		headTreeHash, err := c.resolveHeadTree(ctx, gitRepo)
		if err != nil {
			return binding.GitRepoSnapshot{}, err
		}
		gitRepo.cachedHeadTree = headTreeHash
		gitRepo.cachedGitDirHash = gitDirHash
	}

	indexSnapHash, err := c.SnapIndex(ctx, gitRepo)
	if err != nil {
		return binding.GitRepoSnapshot{}, fmt.Errorf("failed to snap index for repo %s: %w", gitRepo.workDir, err)
	}

	snapshot := binding.GitRepoSnapshot{
		WorkDir:     gitRepo.workDir,
		GitDir:      replaceWorkDir(gitRepo.gitDir, gitRepo.workDir),
		ObjectsDir:  replaceWorkDir(gitRepo.objectsDir, gitRepo.workDir),
		WorkDirHash: workDirHash,
		PacksHash:   packs,
		GitDirHash:  gitDirHash,
		IndexHash:   indexSnapHash,
	}
	return snapshot, nil
}

func replaceWorkDir(dir, workDir string) string {
	if strings.HasPrefix(dir, workDir) {
		return strings.Replace(dir, workDir, "{workdir}", 1)
	}
	return strings.Replace(dir, "/"+workDir, "/{workdir}", 1)
}

func (c *SnapshotService) syncDir(ctx context.Context, dir string, targetHash entities.Hash) error {
	dir = path.Join(c.mountPoint, dir)

	curHash, err := bindcas.Hash(dir)
	if err != nil {
		return err
	}
	if curHash == entities.EmptyDirHash {
		if err := bindcas.SetHash(dir, targetHash); err != nil {
			return err
		}
	} else {
		if err := dirsync.DirSync(ctx, c.store, dir, curHash, targetHash); err != nil {
			return err
		}
	}
	afterHash, err := bindcas.Hash(dir)
	if err != nil {
		return err
	}
	if targetHash != afterHash {
		log.Printf("synced path %s from %s to %s, but got %s after sync", dir, curHash, targetHash, afterHash)
	}
	return nil
}

func (c *SnapshotService) syncWorkDir(ctx context.Context, dir string, deepTree *gitobjects.DeepTreeObject) error {
	hash, err := c.casfs.Hash(dir)
	if err != nil {
		return err
	}
	if hash == entities.EmptyDirHash {
		return c.casfs.SetDeepTree(dir, deepTree.Hash)
	}
	return c.syncDir(ctx, dir, deepTree.Entries[0].Hash)
}

func (c *SnapshotService) deepTreeForTree(ctx context.Context, treeHash entities.Hash) (*gitobjects.DeepTreeObject, error) {
	req := &binding.DeepTreeRequest{
		Version: binding.CurrentDeepTreeRequestVersion,
		Tree:    treeHash,
	}
	if err := req.Encode(); err != nil {
		return nil, err
	}
	for i := range 2 {
		if i == 1 {
			if err := c.store.RefreshMapResults(ctx, req.Hash); err != nil {
				return nil, err
			}
		}
		if res, err := c.store.MapResults(ctx, req.Hash); err != nil {
			return nil, err
		} else if len(res) > 0 {
			return binding.FetchObject[gitobjects.DeepTreeObject](ctx, c.store, res[0])
		}
	}
	return nil, entities.NotExistErrf("no deeptree found for %s", treeHash)
}

func (c *SnapshotService) restore(ctx context.Context, snapshot binding.Snapshot) error {
	progress := abfsrepo.NewProgress("Restore:", len(snapshot.GitRepos)+1)
	defer progress.Stop()

	progress.StartTask("root")
	err := c.syncDir(ctx, ".", snapshot.Root)
	progress.EndTask("root", err == nil)
	if err != nil {
		return fmt.Errorf("failed to sync workspace: %w", err)
	}

	grp, ctx := errgroup.WithContext(ctx)
	grp.SetLimit(runtime.NumCPU())
	for _, p := range snapshot.GitRepos {
		grp.Go(func() (retErr error) {
			progress.StartTask(p.WorkDir)
			defer func() {
				progress.EndTask(p.WorkDir, retErr == nil)
			}()

			gitDir := strings.Replace(p.GitDir, "{workdir}", p.WorkDir, 1)
			objectsDir := strings.Replace(p.ObjectsDir, "{workdir}", p.WorkDir, 1)

			c.reposLock.Lock()
			r, gitExists := c.gitRepos[p.WorkDir]
			c.reposLock.Unlock()

			if !gitExists {
				r = &gitRepo{
					workDir:    p.WorkDir,
					gitDir:     gitDir,
					objectsDir: objectsDir,
				}
			}

			//TODO check if git-dir or objectsDir have changed

			if !gitExists {
				if err := workspaces.SetupEmptyGitRepos(c.casfs, c.mountPoint, p.WorkDir, gitDir, objectsDir); err != nil {
					return fmt.Errorf("failed to setup empty git repo for %s: %w", p.WorkDir, err)
				}
			}

			deeptree, err := binding.DeepTreeForTree(ctx, p.WorkDirHash, c.store)
			if err != nil {
				return err
			}
			if err := c.syncWorkDir(ctx, p.WorkDir, deeptree); err != nil {
				return fmt.Errorf("failed to sync git workdir %s: %w", p.WorkDir, err)
			}

			if c.mountDetails.Config.GitPrefetchSymlinks {
				if err := binding.PrefetchSymlinks(ctx, deeptree, c.store, c.cacheman); err != nil {
					log.Printf("Failed to prefetch symlinks for %s: %v", p.WorkDir, err)
				}
			}
			if err := c.syncDir(ctx, gitDir, p.GitDirHash); err != nil {
				return fmt.Errorf("failed to sync git-dir %s: %w", gitDir, err)
			}
			_ = os.Remove(filepath.Join(gitDir, "index.lock"))

			packsDir := path.Join(objectsDir, "pack")
			if err := c.syncDir(ctx, packsDir, p.PacksHash); err != nil {
				return fmt.Errorf("failed to sync git pack dir %s: %w", packsDir, err)
			}

			if err := c.touchIndices(packsDir); err != nil {
				return fmt.Errorf("failed to touch indices for repo %s: %w", p.WorkDir, err)
			}

			if c.mountDetails.Config.GitPrefetchIndices {
				if err := binding.PrefetchDirs([]string{filepath.Join(packsDir, "*.idx")}, c.cacheman, binding.ContentPrefetchPriority); err != nil {
					log.Printf("Content prefetch failed: %v", err)
				}
			}

			if p.GitDirHash != r.cachedGitDirHash {
				headTreeHash, err := c.resolveHeadTree(ctx, r)
				if err != nil {
					return err
				}
				r.cachedHeadTree = headTreeHash
				r.cachedGitDirHash = p.GitDirHash
			}

			if err := c.RestoreIndex(ctx, p.IndexHash, r); err != nil {
				return fmt.Errorf("failed to restore staging index for %s: %w", p.WorkDir, err)
			}

			c.reposLock.Lock()
			c.gitRepos[p.WorkDir] = r
			c.reposLock.Unlock()

			return nil
		})
	}

	if err := grp.Wait(); err != nil {
		return err
	}

	return nil
}

func (c *SnapshotService) touchIndices(packsPath string) (retErr error) {
	d, err := os.Open(packsPath)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(d, &retErr)

	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		var ts int64
		var packType, id string

		_, _ = fmt.Sscanf(name, "%d-%s-%s", &ts, &packType, &id)
		if ts == 0 {
			continue
		}

		mtime := time.Unix(ts, 0)

		if err := os.Chtimes(path.Join(packsPath, name), mtime, mtime); err != nil {
			return err
		}
	}
	return nil
}
