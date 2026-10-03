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

package gitpusher

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/fastgit"
	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/shared/dagsync"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
)

const (
	printStatusInterval     = 1 * time.Minute
	minStatusReportCooldown = 20 * time.Minute
	pushedRefsFile          = "pushed-refs"
)

type PushState int

const (
	PushStateCreated PushState = iota
	PushStateInitialized
	PushStateSleep
	PushStateFetch
	PushStateDiff
	PushStatePushBlobs
	PushStatePushObjects
	PushStatePushPacks
)

func (s PushState) String() string {
	switch s {
	case PushStateCreated:
		return "created"
	case PushStateInitialized:
		return "initialized"
	case PushStateSleep:
		return "sleep"
	case PushStateFetch:
		return "fetch"
	case PushStateDiff:
		return "diff"
	case PushStatePushBlobs:
		return "push-blobs"
	case PushStatePushObjects:
		return "push-objects"
	case PushStatePushPacks:
		return "push-packs"
	default:
		return fmt.Sprintf("unknown state: %d", s)
	}
}

type pusher struct {
	config     *ProjectConfig
	remoteName string
	abfsClient remote.RemoteCloser
	// store should only be used for writing because there is no local cacheman running
	// on the gitpusher instances. Use objAccesser for reads.
	store          entities.CASStore
	dirPath        string
	pushedRefsFile string
	shgit          *shellgit.ShellGit
	objAccesser    *dagSyncObjectAccessor
	pusherName     string

	state          PushState
	dagSync        *dagsync.DAGSync
	packPusher     *PackPusher
	pushStart      time.Time
	lastPackPushed time.Time
	lastPushed     time.Time
	syncDuration   time.Duration
	lastErr        error
	lastStatus     *binding.ProjectStatus

	pushedRefs      map[string]entities.Hash
	newRefs         map[string]entities.Hash
	newBlobs        []entities.Hash
	newObjects      []entities.Hash
	newCommits      map[entities.Hash]string
	deepTreeMapLock sync.Mutex
	deepTreeMap     map[entities.Hash]entities.Hash // from tree hash to its deeptree hash

	// We don't persist this to disk, but the number of LFS backed
	// objects should be a drop in the bucket compared to an entire android tree
	// On process restart, already pushed LFS objects will get retried but the
	// server will short circuit.
	pushedLFSRefs map[string]struct{}
}

func newPusher(config *ProjectConfig, g *GitPusher, pusherName string) *pusher {
	dirPath := path.Join(g.projectStoragePath, "projects", config.Name().String()+".git")

	p := &pusher{
		config:         config,
		remoteName:     defaultRemoteName,
		abfsClient:     g.limitedRemote,
		store:          remote.NewRemoteStore(g.limitedRemote),
		dirPath:        dirPath,
		pushedRefsFile: path.Join(dirPath, pushedRefsFile),
		state:          PushStateCreated,
		shgit:          g.shgit,
		pusherName:     pusherName,
		deepTreeMap:    make(map[entities.Hash]entities.Hash),
	}
	return p
}

func (s *pusher) String() string {
	return s.config.Name().String()
}

func (s *pusher) Stat() PusherStat {
	ds := s.dagSync
	var dagStat *dagsync.Stats
	if ds != nil {
		stat := ds.Stats()
		dagStat = &stat
	}
	lastErr := ""
	if s.lastErr != nil {
		lastErr = s.lastErr.Error()
	}
	return PusherStat{
		Name:         s.config.Name(),
		Remote:       s.remoteName,
		URL:          s.config.URL,
		State:        s.state.String(),
		UploadStart:  s.pushStart.Format(time.RFC3339),
		LastError:    lastErr,
		SyncDuration: s.syncDuration.Seconds(),
		DagSync:      dagStat,
	}
}

func (s *pusher) setInitStatus(ctx context.Context) error {
	status, err := binding.FetchProjectStatusFromStore(ctx, s.config.Server, s.config.Project, s.store)
	if err != nil {
		return err
	}
	s.lastStatus = status
	s.lastPushed = status.LastSuccess
	if status.LastError != "" {
		s.lastErr = errors.New(status.LastError)
	}
	return nil
}

func (s *pusher) init() error {
	if err := s.setInitStatus(context.Background()); err != nil {
		return err
	}

	if err := ensureGitRepo(s.dirPath); err != nil {
		return err
	}

	packPusher, err := NewPackPusher(s.abfsClient, s.config.Server, s.config.Project, s.dirPath, s.shgit, s.config.PackingStrategy)
	if err != nil {
		return fmt.Errorf("failed to create the git pack pusher: %w", err)
	}
	s.packPusher = packPusher

	if err := s.writeGitConfig(); err != nil {
		return fmt.Errorf("failed to write git config: %w", err)
	}

	s.state = PushStateInitialized
	return nil
}

func (s *pusher) writeGitConfig() error {
	var specs strings.Builder
	for _, spec := range s.config.FetchRefSpecs {
		fmt.Fprintf(&specs, "\tfetch = %s\n", spec)
	}

	template := `# Generated by GitPusher
[core]
	repositoryformatversion = 0
	filemode = true
	bare = true
[remote "%s"]
	url = %s
%s
`
	config := fmt.Sprintf(template, s.remoteName, s.config.URL, specs.String())

	return os.WriteFile(path.Join(s.dirPath, "config"), []byte(config), 0644)
}

func (s *pusher) Push(ctx context.Context) (returnErr error) {
	defer func() {
		s.lastErr = returnErr
		if err := s.putProjectStatus(ctx); err != nil {
			log.Printf("failed to put project status for %s: %v", s.config.Name(), err)
			if s.lastErr == nil {
				returnErr = fmt.Errorf("failed to put project status for %s: %w", s.config.Name(), err)
				s.lastErr = returnErr
			}
		}
	}()

	if s.state == PushStateCreated {
		if err := s.init(); err != nil {
			return err
		}
	}

	s.pushStart = time.Now()
	defer func() {
		s.syncDuration = time.Since(s.pushStart)
		s.state = PushStateSleep
	}()

	s.state = PushStateFetch
	if err := fetchWithRetry(s.shgit, s.dirPath); err != nil {
		return fmt.Errorf("failed to fetch: %w", err)
	}

	s.state = PushStateDiff
	if err := s.refreshRefObjects(); err != nil {
		return err
	}

	if err := s.syncSymRefs(ctx); err != nil {
		return err
	}

	blobsToPush, err := missingHashes(ctx, s.abfsClient, s.newBlobs)
	if err != nil {
		return err
	}
	objectsToPush, err := missingHashes(ctx, s.abfsClient, s.newObjects)
	if err != nil {
		return err
	}

	refHashes := make([]entities.RefHash, 0)
	for r, h := range s.newRefs {
		if s.pushedRefs[r] != h {
			refHashes = append(refHashes, entities.RefHash{Ref: r, Hash: h})
		}
	}
	for r := range s.pushedRefs {
		if _, ok := s.newRefs[r]; !ok {
			refHashes = append(refHashes, entities.RefHash{Ref: r, Hash: entities.ZeroHash})
		}
	}

	// getting a new repo now so it gets the updated git data on disk
	repo, err := fastgit.Open(s.dirPath)
	if err != nil {
		return err
	}
	s.objAccesser = &dagSyncObjectAccessor{repo: repo}
	defer func() {
		if err := repo.Close(); err != nil {
			log.Printf("failed to close repo %s: %v", s.dirPath, err)
		}
		s.objAccesser = nil
	}()

	if err := s.pushObjectRefs(ctx, blobsToPush, objectsToPush, refHashes); err != nil {
		return err
	}

	s.pushedRefs = s.newRefs
	if err := s.writePushedRefs(); err != nil {
		return fmt.Errorf("failed to write pushed-refs file: %w", err)
	}

	if err := s.saveTreeHelperObjects(ctx); err != nil {
		return err
	}

	if time.Since(s.lastPackPushed) > s.config.PackPushInterval {
		s.state = PushStatePushPacks
		if err := s.packPusher.Push(ctx); err != nil {
			return err
		}
		s.lastPackPushed = time.Now()
	}

	s.lastPushed = time.Now()
	log.Printf("successfully processed %s", s)
	return nil
}

func (s *pusher) shouldStoreNewStatus(status *binding.ProjectStatus) bool {
	if s.lastStatus.Pusher != status.Pusher {
		return true
	}
	if s.lastStatus.LastError != status.LastError {
		return true
	}
	if s.lastStatus.LFS != status.LFS {
		return true
	}
	if status.LastSuccess.Sub(s.lastStatus.LastSuccess) > minStatusReportCooldown {
		return true
	}
	if status.LastUpdate.Sub(s.lastStatus.LastUpdate) > minStatusReportCooldown {
		return true
	}
	return false
}

func (s *pusher) putProjectStatus(ctx context.Context) error {
	status := &binding.ProjectStatus{
		Version:     binding.CurrentProjectStatusVersion,
		Pusher:      s.pusherName,
		LFS:         s.config.LFS,
		LastSuccess: s.lastPushed,
		LastUpdate:  time.Now(),
		LastError:   "",
	}
	if s.lastErr != nil {
		status.LastError = s.lastErr.Error()
	}
	if !s.shouldStoreNewStatus(status) {
		return nil
	}
	if err := status.Encode(); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if err := s.abfsClient.PutObject(ctx, status.Hash, remote.ContentReaderFromBytes(status.Encoded)); err != nil {
		return fmt.Errorf("put %s: %w", status.Hash, err)
	}
	if err := s.abfsClient.PutRefs(ctx, s.config.Server, s.config.Project, []entities.RefHash{{Ref: binding.ProjectStatusRef, Hash: status.Hash}}); err != nil {
		return fmt.Errorf("put ref: %w", err)
	}
	s.lastStatus = status
	return nil
}

func (s *pusher) syncSymRefs(ctx context.Context) error {
	targets, refs, err := s.shgit.ListSymRefs(s.config.URL)
	if err != nil {
		return err
	}
	for i, ref := range refs {
		if err := s.updateSymRef(ctx, ref, targets[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *pusher) updateSymRef(ctx context.Context, ref, target string) error {
	refObj := &gitobjects.SymRefObject{
		Content: target,
	}
	if err := refObj.Encode(); err != nil {
		return fmt.Errorf("failed to encode sym ref %q: %w", ref, err)
	}

	refs, _ := s.abfsClient.GetRefs(ctx, s.config.Server, s.config.Project, []string{ref})
	var oldValue entities.Hash
	if len(refs) > 0 {
		oldValue = refs[0]
	}
	if oldValue == refObj.Hash {
		return nil
	}
	if err := s.abfsClient.PutObject(ctx, refObj.Hash, remote.ContentReaderFromBytes(refObj.Encoded)); err != nil {
		return fmt.Errorf("failed to put sym object ref %q (%s): %w", ref, refObj.Hash, err)
	}
	if err := s.abfsClient.PutRefs(ctx, s.config.Server, s.config.Project, []entities.RefHash{{Ref: ref, Hash: refObj.Hash}}); err != nil {
		return fmt.Errorf("failed to put sym ref %q (%s): %w", ref, refObj.Hash, err)
	}
	log.Printf("updated sym ref %q to %q (%s -> %s)", ref, target, oldValue, refObj.Hash)
	return nil
}

func missingHashes(ctx context.Context, client remote.RemoteProvider, hashes []entities.Hash) ([]entities.Hash, error) {
	headers, err := client.Headers(ctx, hashes)
	if err != nil {
		return nil, err
	}
	missings := make([]entities.Hash, 0, len(headers))
	for i, hdr := range headers {
		if hdr.IsEmpty() {
			missings = append(missings, hashes[i])
		}
	}
	return missings, nil
}

func (s *pusher) pushSingleLFSObject(ctx context.Context, client remote.RemoteProvider, lfsOID, lfsPath string) (retErr error) {
	// abfsHash includes blob header
	abfsHash, size, err := objects.HashFileBlob(lfsPath)
	if err != nil {
		return err
	}
	file, err := os.Open(lfsPath)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(file, &retErr)

	// Push the real file.
	if err := client.PutObject(ctx, abfsHash, remote.NewBlobReader(file, size)); err != nil {
		return err
	}

	// Create and push the LFS request.
	lfsReq := &binding.LFSRequest{LFSOID: lfsOID}
	if err := lfsReq.Encode(); err != nil {
		return err
	}
	if err := client.PutObject(ctx, entities.Hash(lfsReq.Hash), remote.ContentReaderFromBytes(lfsReq.Encoded)); err != nil {
		return err
	}

	// Create and push the LFS response.
	lfsRsp := &binding.LFSResult{ABFSOID: abfsHash, LFSRequest: lfsReq.Hash}
	if err := lfsRsp.Encode(); err != nil {
		return err
	}
	if err := client.PutObject(ctx, entities.Hash(lfsRsp.Hash), remote.ContentReaderFromBytes(lfsRsp.Encoded)); err != nil {
		return err
	}

	// Push the map.
	err = client.AddMapResult(ctx, lfsReq.Hash, lfsRsp.Hash)
	if err == nil {
		s.pushedLFSRefs[lfsOID] = struct{}{}
	}

	return err
}

// pushLFSObjects will push the following:
// * The real file pointed to by the pointerfile
// * A LFS request and response object
// * A mapping between the request and the response
// Dagsync is not used here because syncProject uses accessor implementations
// that use core-git and not git-lfs.
func (s *pusher) pushLFSObjects(ctx context.Context, client remote.RemoteProvider) error {
	if err := s.shgit.LFSFetch(s.dirPath); err != nil {
		return fmt.Errorf("lfs fetch failed: %v", err)
	}

	// This can be a slow call because 'git lfs' does not support negative
	// ref specs.
	pathToOID, err := s.shgit.LFSFilesAll(s.dirPath)
	if err != nil {
		return err
	}

	// lfs files are stored in lfs/objects/
	// They do not have any headers and they are not compressed by git
	for _, oids := range pathToOID {
		for _, oid := range oids {
			if _, pushed := s.pushedLFSRefs[oid]; pushed {
				continue
			}

			lfsPath := path.Join(s.dirPath, "lfs", "objects", oid[0:2], oid[2:4], oid)
			if err := s.pushSingleLFSObject(ctx, client, oid, lfsPath); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *pusher) pushObjectRefs(ctx context.Context, newBlobs, newObjects []entities.Hash, refHashes []entities.RefHash) error {
	s.state = PushStatePushBlobs
	ds := dagsync.New(&dagsync.DagSyncOpts{
		Updater:      s.abfsClient,
		Accessor:     s.objAccesser,
		WorkersCount: s.config.Workers,
	})
	// We don't care much about the maintenance operations since we will create a new dagsync every time.
	// Running too frequently (default is every 5 seconds) wastes a lot of loop cycles while dagsync is locked.
	ds.MaintenanceRunInterval = 24 * time.Hour
	s.dagSync = ds
	defer func() {
		s.dagSync = nil
	}()

	ticker := time.NewTicker(printStatusInterval)
	defer ticker.Stop()
	dagsyncCtx, stopDagsync := context.WithCancel(ctx)
	defer stopDagsync()
	go func() {
		for {
			select {
			case <-ticker.C:
				stats := ds.Stats()
				log.Printf("%s still uploading. Current states: %v", s, stats.States)
			case <-dagsyncCtx.Done():
				return
			}
		}
	}()

	go ds.Start(dagsyncCtx)

	if s.config.LFS {
		if err := s.pushLFSObjects(ctx, s.abfsClient); err != nil {
			// We only make best effort to push LFS objects. Errors are logged but
			// we continue.
			log.Printf("could not push LFS objects: %v", err)
		}
	}

	// Push the blobs first, since they can be pushed without any dependencies
	log.Printf("%s blobs: %d found, %d needed upload", s, len(s.newBlobs), len(newBlobs))
	for i, h := range newBlobs {
		ds.PutBlob(h)
		if (i+1)%100000 == 0 {
			log.Printf("%s: %d out of %d blob added to dagsync", s, i+1, len(newBlobs))
		}
	}

	if err := ds.Wait(48 * time.Hour); err != nil {
		return fmt.Errorf("timed out waiting for uploads to complete: %w", err)
	}

	s.state = PushStatePushObjects
	log.Printf("%s objects: %d found, %d needed upload", s, len(s.newObjects), len(newObjects))
	for i, h := range newObjects {
		ds.PutObject(h)
		if (i+1)%100000 == 0 {
			log.Printf("%s: %d out of %d objects added to dagsync", s, i+1, len(newObjects))
		}
	}

	log.Printf("%s refs: %d refs need to be updated", s, len(refHashes))
	ds.PutRefs(s.config.Server, s.config.Project, refHashes)

	if err := ds.Wait(48 * time.Hour); err != nil {
		return fmt.Errorf("timed out waiting for uploads to complete: %w", err)
	}
	return nil
}

const errBadObject = "fatal: bad object"

func (s *pusher) isBadObjectError(err error) bool {
	return err != nil && strings.Contains(err.Error(), errBadObject)
}

func (s *pusher) refreshRefObjects() error {
	s.pushedRefs = nil
	s.newRefs = nil
	s.newBlobs = nil
	s.newObjects = nil
	if s.pushedLFSRefs == nil {
		s.pushedLFSRefs = make(map[string]struct{})
	}

	for i := 0; i < 2; i++ {
		err := s.doRefreshRefObjects()
		if err != nil {
			if s.isBadObjectError(err) && i == 0 {
				log.Printf("Warning: bad object encountered in rev-list for %s. Deleting pushed-refs and retrying. Error: %v", s, err)
				if err := os.Remove(s.pushedRefsFile); err != nil && !os.IsNotExist(err) {
					log.Printf("Failed to delete pushed-refs file: %v", err)
				}
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("failed to refresh ref objects after retry")
}

func (s *pusher) doRefreshRefObjects() error {
	pushedRefs, err := readPushedRefs(s.pushedRefsFile)
	if err != nil {
		return fmt.Errorf("failed to read pushed-refs file: %w", err)
	}
	s.pushedRefs = pushedRefs

	newRefs, err := s.shgit.ShowRefs(s.dirPath)
	if err != nil {
		return fmt.Errorf("failed to get new refs: %w", err)
	}
	s.newRefs = newRefs

	if refsEqual(pushedRefs, newRefs) {
		log.Printf("%s had no ref changes", s)
		s.newCommits = make(map[entities.Hash]string)
		return nil
	}

	oldHashes := make(map[entities.Hash]struct{}, 0)
	for _, h := range pushedRefs {
		oldHashes[h] = struct{}{}
	}
	newHashes := make(map[entities.Hash]string, 0)
	for ref, h := range newRefs {
		if _, ok := oldHashes[h]; !ok {
			newHashes[h] = ref
		}
	}
	s.newCommits = newHashes

	includedHashes := make([]entities.Hash, 0)
	for hash := range newHashes {
		includedHashes = append(includedHashes, hash)
	}
	excludedHashes := make([]entities.Hash, 0)
	for hash := range oldHashes {
		excludedHashes = append(excludedHashes, hash)
	}

	rev, err := s.shgit.RevListObjects(s.dirPath, includedHashes, excludedHashes)
	if err != nil {
		return err
	}
	s.newBlobs = rev.Blobs
	s.newObjects = rev.Objects
	return nil
}

func refsEqual(a, b map[string]entities.Hash) bool {
	return reflect.DeepEqual(a, b)
}

func (s *pusher) writePushedRefs() error {
	return writePushedRefs(s.pushedRefsFile, s.pushedRefs)
}

func writePushedRefs(fileName string, pushedRefs map[string]entities.Hash) (retErr error) {
	file, err := os.OpenFile(fileName+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			entities.CloseErrCheck(file, &retErr)
		}
	}()

	for ref, hash := range pushedRefs {
		_, err = fmt.Fprintln(file, hash, ref)
		if err != nil {
			return err
		}
	}

	closed = true
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(fileName+".tmp", fileName)
}

func readPushedRefs(fileName string) (result map[string]entities.Hash, retErr error) {
	result = make(map[string]entities.Hash)

	file, err := os.Open(fileName)
	if err != nil {
		if entities.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}
	defer entities.CloseErrCheck(file, &retErr)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		fields := strings.SplitN(scanner.Text(), " ", 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("unexpected number of fields in %q", scanner.Text())
		}
		hash := entities.NewHash(fields[0])
		result[fields[1]] = hash
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *pusher) SyncDump(ctx context.Context, w io.Writer) {
	if s.dagSync == nil {
		_, _ = fmt.Fprintf(w, "%s: dagSync is nil. It is not running yet.\n", s)
		return
	}
	s.dagSync.Dump(ctx, w)
}

func (s *pusher) SyncValidate(ctx context.Context, w io.Writer, nodeStaleTimeout time.Duration) {
	if s.dagSync == nil {
		_, _ = fmt.Fprintf(w, "%s: dagSync is nil. It is not running yet.\n", s)
		return
	}
	s.dagSync.Validate(ctx, w, nodeStaleTimeout)
}

// saveTreeHelperObjects saves deep-tree and digest-map helper objects
func (s *pusher) saveTreeHelperObjects(ctx context.Context) error {
	commits := make([]entities.Hash, 0, len(s.newRefs))
	refs := make([]string, 0, len(s.newRefs))
	for h, ref := range s.newCommits {
		if h == entities.ZeroHash || !strings.HasPrefix(string(ref), "refs/heads/") {
			continue
		}
		refs = append(refs, ref)
		commits = append(commits, h)
	}
	treeHashes, err := s.shgit.CommitTreeHashes(s.dirPath, commits)
	if err != nil {
		return err
	}

	start := time.Now()
	if err := s.saveDeepTree(ctx, refs, treeHashes); err != nil {
		return err
	}
	log.Printf("Saving deep-tree for %s took %s", s.config.Name(), time.Since(start))

	start = time.Now()
	if err := s.saveDigestMap(ctx, refs, treeHashes); err != nil {
		return err
	}
	log.Printf("Saving digest-map for %s took %s", s.config.Name(), time.Since(start))

	return nil
}

func (s *pusher) saveDigestMap(ctx context.Context, refs []string, treeHashes []entities.Hash) error {
	if len(treeHashes) > 0 {
		log.Printf("Updating %d digest-map objects for %s", len(treeHashes), s.config.Name())
	}

	// we are just using a cache local to this run because it will have every blob and symlink so we don't
	// want to keep this in memory forever
	var sha256CacheLock sync.Mutex
	sha256Cache := make(map[entities.Hash]entities.SHA256)

	grp, ctx := errgroup.WithContext(ctx)
	grp.SetLimit(runtime.NumCPU())
	for i, t := range treeHashes {
		grp.Go(func() error {
			start := time.Now()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			request := &binding.DigestMapRequest{
				Version: binding.CurrentDigestMapRequestVersion,
				Tree:    t,
			}
			if err := request.Encode(); err != nil {
				return fmt.Errorf("failed to encode digest map request for ref %s (%s): %w", refs[i], t, err)
			}
			result, err := s.store.MapResults(ctx, request.Hash)
			if err != nil {
				return fmt.Errorf("failed to fetch digest map info for ref %s (%s): %w", refs[i], t, err)
			}
			if len(result) > 0 {
				return nil
			}
			digestMap, err := s.digestMapForTree(ctx, t, &sha256CacheLock, sha256Cache)
			if err != nil {
				return fmt.Errorf("failed to build digest map for ref %s (%s): %w", refs[i], t, err)
			}
			if err := digestMap.Encode(); err != nil {
				return fmt.Errorf("failed to encode digest map for ref %s (%s): %w", refs[i], t, err)
			}
			if err := s.store.PutObject(ctx, digestMap.Hash, digestMap.Encoded); err != nil {
				return fmt.Errorf("failed to store digest map %s for ref %s: %w", digestMap.Hash, refs[i], err)
			}
			if err := s.store.PutObject(ctx, request.Hash, request.Encoded); err != nil {
				return fmt.Errorf("failed to store digest map request %s for ref %s: %w", request.Hash, refs[i], err)
			}
			if err := s.abfsClient.AddMapResult(ctx, request.Hash, digestMap.Hash); err != nil {
				return fmt.Errorf("failed to add digest map result from request %s to digest map %s for ref %s: %w", request.Hash, digestMap.Hash, refs[i], err)
			}
			log.Printf("Added digest map for %s ref %s: %s -> %s in %s", s.config.Name(), refs[i], request.Hash, digestMap.Hash, time.Since(start))
			return nil
		})
	}

	return grp.Wait()
}

func (s *pusher) saveDeepTree(ctx context.Context, refs []string, treeHashes []entities.Hash) error {
	if len(treeHashes) > 0 {
		log.Printf("Updating %d deep-tree objects for %s", len(treeHashes), s.config.Name())
	}

	grp, ctx := errgroup.WithContext(ctx)
	grp.SetLimit(runtime.NumCPU())
	for i, t := range treeHashes {
		grp.Go(func() error {
			start := time.Now()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			request := &binding.DeepTreeRequest{
				Version: binding.CurrentDeepTreeRequestVersion,
				Tree:    t,
			}
			if err := request.Encode(); err != nil {
				return fmt.Errorf("failed to encode deeptree request for ref %s (%s): %w", refs[i], t, err)
			}
			result, err := s.store.MapResults(ctx, request.Hash)
			if err != nil {
				return fmt.Errorf("failed to fetch deeptree info for ref %s (%s): %w", refs[i], t, err)
			}
			if len(result) > 0 {
				// cache this deep tree in case a different calculation needs it later.
				s.deepTreeMapLock.Lock()
				defer s.deepTreeMapLock.Unlock()
				s.deepTreeMap[t] = result[0]
				return nil
			}
			entries, err := s.deepTreeForTree(ctx, t)
			if err != nil {
				return fmt.Errorf("failed to calculate deeptree for ref %s (%s): %w", refs[i], t, err)
			}
			dt, err := s.putDeepTree(ctx, entries)
			if err != nil {
				return fmt.Errorf("failed to add deep-tree %s for ref %s: %w", dt, refs[i], err)
			}
			if err := s.store.PutObject(ctx, request.Hash, request.Encoded); err != nil {
				return fmt.Errorf("failed to add deep-tree request %s for ref %s: %w", request.Hash, refs[i], err)
			}
			if err := s.abfsClient.AddMapResult(ctx, request.Hash, dt); err != nil {
				return fmt.Errorf("failed to add deep-tree result from request %s to deep-tree %s for ref %s: %w", request.Hash, dt, refs[i], err)
			}
			log.Printf("Added deeptree for %s ref %s: %s -> %s in %s", s.config.Name(), refs[i], request.Hash, dt, time.Since(start))
			return nil
		})
	}

	return grp.Wait()
}

func (s *pusher) putDeepTree(ctx context.Context, entries []*entities.DeepEntry) (entities.Hash, error) {
	deepTree := &gitobjects.DeepTreeObject{DeepTree: entities.DeepTree{Entries: entries}}
	if err := deepTree.Encode(); err != nil {
		return entities.Hash{}, err
	}
	if err := s.store.PutObject(ctx, deepTree.Hash, deepTree.Encoded); err != nil {
		return entities.Hash{}, err
	}

	s.deepTreeMapLock.Lock()
	defer s.deepTreeMapLock.Unlock()
	s.deepTreeMap[entries[0].Hash] = deepTree.Hash
	return deepTree.Hash, nil
}

func (s *pusher) deepTreeForTree(ctx context.Context, tree entities.Hash) ([]*entities.DeepEntry, error) {
	entries, err := s.shgit.ListTreeRec(s.dirPath, tree.String())
	if err != nil {
		return nil, err
	}

	rootNode := s.treeToTreeNode(tree, entries)

	var resultEntries []*entities.DeepEntry
	resultEntries = append(resultEntries, &entities.DeepEntry{
		Mode: entities.FileModeDir,
		Hash: rootNode.hash,
	})

	var dfs func(node *treeNode, entry *entities.DeepEntry) error
	dfs = func(node *treeNode, entry *entities.DeepEntry) error {
		entry.Size++
		for _, childNode := range node.children {
			start := len(resultEntries)
			childEntry := &entities.DeepEntry{
				Name: childNode.name,
				Mode: childNode.mode,
				Hash: childNode.hash,
			}
			resultEntries = append(resultEntries, childEntry)
			if childNode.mode == entities.FileModeDir {
				s.deepTreeMapLock.Lock()
				cachedDT, ok := s.deepTreeMap[childNode.hash]
				s.deepTreeMapLock.Unlock()
				if ok {
					childEntry.Mode = entities.FileModeDeepTree
					childEntry.Hash = cachedDT
					entry.Size++
					continue
				}
				if err := dfs(childNode, childEntry); err != nil {
					return err
				}
				if childEntry.Size > binding.DeepTreeThreshold {
					childEntry.Name = ""
					hash, err := s.putDeepTree(ctx, resultEntries[start:])
					if err != nil {
						return err
					}
					resultEntries = append(resultEntries[:start], &entities.DeepEntry{
						Name: childNode.name,
						Mode: entities.FileModeDeepTree,
						Hash: hash,
					})
					entry.Size++
				} else {
					entry.Size += childEntry.Size
				}
			} else {
				childEntry.Size = childNode.size
				entry.Size++
			}
		}
		return nil
	}

	if err := dfs(rootNode, resultEntries[0]); err != nil {
		return nil, err
	}

	return resultEntries, nil
}

func (s *pusher) digestMapForTree(ctx context.Context, tree entities.Hash, sha256CacheLock *sync.Mutex, sha256Cache map[entities.Hash]entities.SHA256) (*binding.DigestMap, error) {
	entries, err := s.shgit.ListTreeRec(s.dirPath, tree.String())
	if err != nil {
		return nil, err
	}

	rootNode := s.treeToTreeNode(tree, entries)

	previousDigestMaps := make([]entities.Hash, 0)
	digestMap := &binding.DigestMap{
		Version: binding.CurrentDigestMapVersion,
		Digests: make([]binding.DigestEntry, 0),
	}
	// skip blobs we've already seen in this tree
	seenBlobs := make(map[entities.Hash]struct{})

	var dfs func(node *treeNode) error
	dfs = func(node *treeNode) error {
		for _, childNode := range node.children {
			if childNode.mode == entities.FileModeDir {
				if err := dfs(childNode); err != nil {
					return err
				}
				continue
			} else if !childNode.mode.IsFileOrSymlink() { // excludes submodules
				continue
			}
			if _, seen := seenBlobs[childNode.hash]; seen {
				continue
			}
			seenBlobs[childNode.hash] = struct{}{}
			sha256CacheLock.Lock()
			sha, ok := sha256Cache[childNode.hash]
			sha256CacheLock.Unlock()
			if !ok {
				sha, err = s.sha256(ctx, childNode.hash)
				if err != nil {
					return err
				}
				sha256CacheLock.Lock()
				sha256Cache[childNode.hash] = sha
				sha256CacheLock.Unlock()
			}
			if len(digestMap.Digests) >= binding.DigestMapThreshold {
				// digest map full, so store the current and create new one
				if err := digestMap.Encode(); err != nil {
					return fmt.Errorf("failed to encode full digest map: %w", err)
				}
				if err := s.store.PutObject(ctx, digestMap.Hash, digestMap.Encoded); err != nil {
					return fmt.Errorf("failed to store full digest map: %w", err)
				}
				previousDigestMaps = append(previousDigestMaps, digestMap.Hash)
				digestMap = &binding.DigestMap{
					Version: binding.CurrentDigestMapVersion,
					Digests: make([]binding.DigestEntry, 0),
				}
			}
			digestMap.Digests = append(digestMap.Digests, binding.DigestEntry{
				Hash:   childNode.hash,
				SHA256: sha,
			})
		}
		return nil
	}

	if err := dfs(rootNode); err != nil {
		return nil, err
	}

	digestMap.DigestMaps = previousDigestMaps
	return digestMap, nil
}

func (s *pusher) sha256(ctx context.Context, h entities.Hash) (sha entities.SHA256, returnErr error) {
	// using underlying repo
	rc, err := s.objAccesser.repo.OpenObject(h)
	if err != nil {
		return entities.ZeroSHA256, err
	}
	defer entities.CloseErrCheck(rc, &returnErr)

	// For SHA256 we exclude the header from the hash calculation
	if _, _, err := objects.ReadHeader(rc); err != nil {
		return entities.ZeroSHA256, err
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, rc); err != nil {
		return entities.ZeroSHA256, err
	}
	copy(sha[:], hasher.Sum(nil))
	return sha, nil
}

type treeNode struct {
	name     string
	mode     entities.FileMode
	hash     entities.Hash
	size     int64
	children []*treeNode
}

func (*pusher) treeToTreeNode(tree entities.Hash, entries []entities.DeepEntry) *treeNode {
	rootNode := &treeNode{
		mode: entities.FileModeDir,
		hash: tree,
	}

	nodes := map[string]*treeNode{"": rootNode}

	var getOrCreateParent func(parentPath string) *treeNode
	getOrCreateParent = func(parentPath string) *treeNode {
		if node, ok := nodes[parentPath]; ok {
			return node
		}
		dir, file := path.Split(parentPath)
		grandParentPath := strings.TrimSuffix(dir, "/")
		grandParent := getOrCreateParent(grandParentPath)

		node := &treeNode{
			name: file,
			mode: entities.FileModeDir,
		}
		grandParent.children = append(grandParent.children, node)
		nodes[parentPath] = node
		return node
	}

	for _, entry := range entries {
		dir, file := path.Split(entry.Name)
		parentPath := strings.TrimSuffix(dir, "/")
		parent := getOrCreateParent(parentPath)

		var node *treeNode
		for _, child := range parent.children {
			if child.name == file {
				node = child
				break
			}
		}
		if node == nil {
			node = &treeNode{name: file}
			parent.children = append(parent.children, node)
		}
		node.mode = entry.Mode
		node.hash = entry.Hash
		node.size = entry.Size

		if entry.Mode == entities.FileModeDir {
			nodes[entry.Name] = node
		}
	}
	return rootNode
}
