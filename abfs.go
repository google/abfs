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

package abfs

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/gorilla/mux"
	"github.com/iafan/cwalk"
	"golang.org/x/sync/errgroup"

	"github.com/google/abfs/manifest"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/daemon"
	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/filesystem/factory"
	shellgit "github.com/google/abfs/git"
	"github.com/google/abfs/maps"
	"github.com/google/abfs/repo"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/store"
)

const (
	MountDetailsFileName    = ".abfs.mount-details"
	ABFSManifestsDir        = "abfs-manifests"
	RepoBinDir              = ".repo/repo"
	RepoProjectsDir         = ".repo/projects"
	RepoProjectsObjDir      = ".repo/project-objects"
	DefaultManifestFileName = "default.xml"
	PinnedManifestFileName  = "pinned.xml"
)

const (
	maxUnixSocketPathLength = 108

	minProjectStaleWarn = 30 * time.Minute

	// Name of the remote in the injected manifest project.
	// It is injected as part of project creation.
	// It needs a separate remote because it may be on a host not
	// currently present in the original manifest.
	metaManifestRemote = "abfs-meta-remote"

	keepAliveInterval = 15 * time.Minute

	gitIndicesPrefetchPriority = 0.2
	contentPrefetchPriority    = 0.3
)

type ABFSInfo struct {
	filesystem.MountInfo
	MountPoint            string
	MountDetails          *binding.MountDetails
	MountType             instance.CASFSType
	InitHash              entities.Hash
	DebugConfig           *entities.FSConfig
	ManifestScheme        string
	ManifestServer        string
	ManifestFile          string
	ManifestProjectName   string
	ManifestFileHash      string
	ManifestBranch        string
	ManifestBranchFile    string
	ManifestBytes         []byte
	LocalManifestDir      string
	ChangeIDs             []string
	LFS                   bool
	PrefetchGitIndices    bool
	PrefetchDirs          []string
	PrefetchSymlinks      bool
	Started               func()
	IgnoreMissingProjects bool
	GitRepoInit           bool
	GitRepoSetupDone      bool
	MountGroups           []string
	MountFallbackToGit    bool
	AutoSetup             bool
}

type ABFS struct {
	ABFSInfo
	cacheman                 *client.Client
	mounted                  bool
	cleanups                 []func()
	store                    entities.CASStore
	git                      *casgit.CASGit
	repo                     *repo.Repo
	mnt                      filesystem.Mount
	http                     *filesystem.HTTP
	shgit                    *shellgit.ShellGit
	schemeManProjURL         string
	statusRefs               map[string]entities.Hash
	packRefs                 map[string]entities.Hash
	slowProjects             atomic.Int64
	clientGeneratedDeepTrees atomic.Int64
}

func NewABFS(info ABFSInfo) (*ABFS, error) {
	a := &ABFS{
		ABFSInfo: info,
		mnt:      factory.NewMount(info.MountType),
		cacheman: client.NewClient(info.CacheDir, info.FetchTimeout),
	}
	a.mnt.SetInitConfig(info.DebugConfig)

	if err := a.setupStore(); err != nil {
		return nil, err
	}

	if err := a.setupRepo(); err != nil {
		a.updateAndSaveMountDetails(context.Background(), err)
		return nil, err
	}

	a.shgit = &shellgit.ShellGit{BinPath: "git"}

	schemeManProjURL, err := url.JoinPath(instance.MaybeAddScheme(a.ManifestServer, a.ManifestScheme), a.ManifestProjectName)
	if err != nil {
		return nil, err
	}
	a.schemeManProjURL = schemeManProjURL

	return a, nil
}

func (a *ABFS) writeMountDetails(ctx context.Context) error {
	buf := bytes.NewBuffer(nil)
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	d := &struct {
		*binding.MountDetails
		Hash string
	}{
		a.MountDetails,
		a.MountDetails.Hash.String(),
	}
	if err := enc.Encode(d); err != nil {
		return err
	}

	if err := os.Symlink(path.Join("..", MountDetailsFileName), path.Join(a.MountPoint, ".repo", "mount-details")); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}

	return filesystem.WriteFile(ctx, a.mnt.Root(), MountDetailsFileName, buf)
}

func (a *ABFS) putMountDetails(ctx context.Context) error {
	if err := a.MountDetails.Encode(); err != nil {
		return fmt.Errorf("failed to encode MountDetails: %w", err)
	}
	if err := a.store.PutObject(ctx, a.MountDetails.Hash, a.MountDetails.Encoded); err != nil {
		return fmt.Errorf("failed to put MountDetails with hash %s: %w", a.MountDetails.Hash, err)
	}

	log.Printf("Saved Mount-Details started at %d with hash %s", a.MountDetails.StartTimestamp, a.MountDetails.Hash)
	if a.MountDetails.Error == "" {
		if err := a.writeMountDetails(ctx); err != nil {
			return err
		}
	}

	if a.LocalOnly {
		return nil
	}

	if !a.LocalOnly {
		ref := fmt.Sprintf("refs/abfs/mount/%s/%d", a.MountDetails.MetaRefPrefix, a.MountDetails.StartTimestamp)
		refs := []entities.RefHash{{Ref: ref, Hash: a.MountDetails.Hash}}
		if err := a.store.PutRefs(a.ManifestServer, entities.ABFSMetaProject, refs); err != nil {
			log.Printf("failed to update %s to %s: %s", ref, a.MountDetails.Hash, err)
		}
	}

	return nil
}

func (a *ABFS) updateMountDetails(err error) {
	if err != nil {
		a.MountDetails.Error = err.Error()
	}
	a.MountDetails.MountDuration = time.Since(a.MountDetails.StartTime).Seconds()

	pinnedManifestDir := ABFSManifestsDir
	if h, err := bindcas.Hash(path.Join(a.MountPoint, casgit.RepoDirName, pinnedManifestDir, DefaultManifestFileName)); err != nil {
		if !entities.IsNotExist(err) {
			log.Printf("Could not get the hash of the default manifest: %s", err)
		}
	} else {
		a.MountDetails.ManifestHash = h
	}

	if h, err := bindcas.Hash(path.Join(a.MountPoint, casgit.RepoDirName, pinnedManifestDir, PinnedManifestFileName)); err != nil {
		if !entities.IsNotExist(err) {
			log.Printf("Could not get the hash of the pinned manifest: %s", err)
		}
	} else {
		a.MountDetails.PinnedManifestHash = h
	}

	//TODO add the root hash
}

func (a *ABFS) updateAndSaveMountDetails(ctx context.Context, err error) {
	a.updateMountDetails(err)
	if err := a.putMountDetails(ctx); err != nil {
		log.Printf("Could not put mount-details: %s", err)
	}
}

func (a *ABFS) Serve(ctx context.Context) (err error) {
	cleanCh := make(chan struct{}, 1)
	a.startCleanupWatch(ctx, cleanCh)

	if a.GitRepoInit {
		if a.ManifestBranch == "" {
			return fmt.Errorf("--repo Option only works with manifest branch for now")
		}
		if a.MountType != instance.CASFSTypeKmod {
			return fmt.Errorf("--repo Option only working for kernel module mode now")
		}
	}

	defer func() {
		if !a.mounted {
			// Have a new context for the final update and save of mount-details
			a.updateAndSaveMountDetails(context.Background(), err)
		}
	}()

	if err := a.mnt.Mount(ctx, a.MountPoint, a.MountInfo, a.store); err != nil {
		return err
	}
	a.addCleanup(a.mnt.Unmount)

	// Create channel to monitor status of mount serve. We read from it once but there could be two
	// goroutines writing to it which first one wins. In order to avoid the goroutines leaking we make
	// channel buffered.
	mntChan := make(chan error, 2)
	go func() {
		err := a.mnt.Serve()
		mntChan <- err
	}()

	// Need to wait for serve to be ready, kmod is ready immediately
	// Set[]Attr() will block/retry for fuse
	if err := bindcas.SetHash(a.MountPoint, a.InitHash); err != nil {
		return err
	}
	a.mnt.Ready()

	// abfs git commands need socket pre-setup
	router := mux.NewRouter()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if a.mnt.Config().LogHttp {
			log.Printf("HTTP %s %s", r.Method, r.URL)
		}
		router.ServeHTTP(w, r)
	})

	// Create HTTP interface
	a.http = filesystem.CreateHTTP(a.mnt, a.store)
	router.PathPrefix("/mnt/").Handler(a.http.CreateRouter("/mnt"))
	router.HandleFunc("/chroot/{path:.*}", a.handleChroot).Methods("POST")
	router.HandleFunc("/provider-ping", a.handleProviderPing).Methods("POST")
	router.HandleFunc("/git-repo/{workdir:.*}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods("PUT")

	socketPath := path.Join(a.MountPoint, entities.ABFSSocketFileName)
	go func() {
		err := a.listenUnixSocket(socketPath)
		ctxCanceled := ctx.Err() != nil
		if !ctxCanceled && err != nil {
			log.Print(boostMsg(fmt.Sprintf("ERROR: listenUnixSocket failed: %s", err)))
		}
		// mntChan <- err
	}()

	if err := waitForSocket(socketPath, 5*time.Second); err != nil {
		return err
	}

	if err := a.createManifestFiles(ctx, a.mnt.Root(), a.repo.Manifest(), a.repo.PinnedManifest()); err != nil {
		return err
	}

	log.Printf("Starting to serve the ABFS on %s...", a.MountPoint)
	a.updateAndSaveMountDetails(ctx, nil)
	if err := a.prefetchContent(); err != nil {
		log.Printf("Failed to prefetch content: %v", err)
	}
	a.mounted = true
	if a.AutoSetup {
		cmd := exec.CommandContext(ctx, os.Args[0], "setup", a.MountPoint)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to auto-setup: %v", err)
		}
		log.Printf("ABFS setup completed successfully.")
	}

	if a.Started != nil {
		a.Started()
	}
	if a.RemoteReadOnly {
		go a.startKeepAliveScanAndNotify(ctx)
	}
	err = <-mntChan
	<-cleanCh
	return err
}

func (a *ABFS) prefetchContent() error {
	for _, pattern := range a.PrefetchDirs {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		for _, m := range matches {
			if info, _ := os.Stat(m); info.IsDir() { // ignore error
				if err := a.prefetchDir(m); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (a *ABFS) prefetchDir(dir string) error {
	blobs := make([]entities.Hash, 0)
	err := filepath.Walk(dir, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		hash, _ := bindcas.Hash(p) // ignore errors
		if !hash.IsZero() {
			blobs = append(blobs, hash)
		}
		return nil
	})
	if err != nil {
		return err
	}

	return a.cacheman.Prefetch(blobs, nil, contentPrefetchPriority)
}

func (a *ABFS) startKeepAliveScanAndNotify(ctx context.Context) {
	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()
	touchedCount, err := a.keepAliveScan(ctx)
	if err != nil {
		log.Printf("Keep-alive scan failed: %v", err)
	} else {
		log.Printf("Keep-alive scan complete. Touched %d objects.", touchedCount)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			touchedCount, err := a.keepAliveScan(ctx)
			if err != nil {
				log.Printf("Keep-alive scan failed: %v", err)
			} else {
				log.Printf("Keep-alive scan complete. Touched %d objects.", touchedCount)
				if err := a.store.StartGC(ctx); err != nil {
					log.Printf("Keep-alive scan failed: %v", err)
				}
			}
		}
	}
}

func (a *ABFS) keepAliveScan(ctx context.Context) (int64, error) {
	// TODO: Optimize keep alive scan for multiple nsjail chroot
	log.Println("Starting keep-alive scan of local objects...")
	var touchedCount int64
	skip := func(isDir bool) error {
		if isDir {
			return filepath.SkipDir
		}
		return nil
	}
	err := cwalk.Walk(a.MountPoint, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if filepath.Base(path) == bindcas.GitDirName ||
			filepath.Base(path) == bindcas.ControlDirName {
			return skip(info.IsDir())
		}

		loaded, err := bindcas.Loaded(path)
		if err == nil && !loaded {
			return skip(info.IsDir())
		}

		fullPath := filepath.Join(a.MountPoint, path)
		hash, err := bindcas.Hash(fullPath)
		if err != nil {
			return err
		}

		onRemote, err := a.store.IsObjectRemote(ctx, hash)
		if err != nil {
			return err
		} else if onRemote {
			return skip(info.IsDir())
		}

		err = a.store.KeepAlive(ctx, hash)
		if err != nil {
			return err
		}
		atomic.AddInt64(&touchedCount, 1)
		return nil
	})
	return touchedCount, err
}

func (a *ABFS) handleChroot(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	p := vars["path"]
	ctx := r.Context()

	var chrootRequest binding.ChrootRequest
	if err := json.NewDecoder(r.Body).Decode(&chrootRequest); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected to receive the list of overlays: %s\n", err)
		return
	}

	dir, err := a.mnt.Chroot(r.Context(), p)
	if errors.Is(err, os.ErrExist) {
		_, _ = fmt.Fprintf(w, "chroot already exists\n")
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "error creating chroot %s\n", err)
		return
	}

	overlays := chrootRequest.Overlays
	views := chrootRequest.Views
	if (len(overlays) + len(views)) > 0 {
		workspaceManifest, err := a.repo.WorkspaceManifest(a.MountPoint)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, "error setting up chroot  %s\n", err)
			return
		}

		newMan, err := overlaysMap(a.repo.Manifest(), overlays, views)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, "error creating overlay map: %s\n", err)
			return
		}
		newPinned, err := overlaysMap(workspaceManifest, overlays, views)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, "error creating overlay map for workspace: %s\n", err)
			return
		}

		if err := a.createManifestFiles(ctx, dir, newMan, newPinned); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, "error while creating project directories %s\n", err)
			return
		}
	}

	go func() {
		err := a.listenUnixSocket(path.Join(a.MountPoint, p, entities.ABFSSocketFileName))
		if err != nil {
			log.Print(boostMsg(fmt.Sprintf("ERROR: listenUnixSocket failed: %s", err)))
		}
	}()

	_, _ = fmt.Fprintf(w, "done\n")
}

func (a *ABFS) handleProviderPing(w http.ResponseWriter, r *http.Request) {
	// We never establish the 'c.cacheman' binary connection, so only check http.
	if err := a.cacheman.Ping(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "cacheman http ping failed: %s\n", err)
		return
	}
	if err := a.store.HealthCheck(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "store health check failed: %s\n", err)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "OK\n")
}

func (a *ABFS) startCleanupWatch(ctx context.Context, ch chan struct{}) {
	go func() {
		defer func() {
			close(ch)
		}()

		<-ctx.Done()
		log.Printf("Stopping...")
		for i := len(a.cleanups) - 1; i >= 0; i-- {
			a.cleanups[i]()
		}
	}()
}

func (a *ABFS) addCleanup(cleanup func()) {
	a.cleanups = append(a.cleanups, cleanup)
}

func (a *ABFS) setupStore() error {
	store, err := store.NewCASStore(a.CacheDir, a.LocalOnly, a.FetchTimeout)
	if err != nil {
		return err
	}
	a.addCleanup(func() {
		if err := store.Close(); err != nil {
			log.Printf("Failed to close store: %v", err)
		}
	})
	a.store = store
	a.git = casgit.New(store)
	return nil
}

func (a *ABFS) listenUnixSocket(p string) error {
	if len(p) >= maxUnixSocketPathLength {
		log.Print(boostMsg("WARNING: unix socket path length likely too long"))
	}
	unixListener, err := net.Listen("unix", p)
	if err != nil {
		return err
	}
	server := daemon.SelfStoppingServer{
		Server: http.Server{
			Handler: http.DefaultServeMux,
		},
		Done:       make(chan struct{}),
		SocketPath: p,
	}
	a.addCleanup(func() {
		log.Printf("Shutdown domain socket server %s...", p)
		if err := server.Shutdown(context.Background()); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Failed to shutdown domain socket server %s: %s", p, err)
		}
	})
	err = server.Serve(unixListener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// waitForSocket waits for socket at path to be alive until specified timeout
func waitForSocket(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			return conn.Close()
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timeout exceeded while waiting for socket: %s", socketPath)
}

func forEachProject(ctx context.Context, projects []*manifest.Project, groups []string, callback func(*manifest.Project) error) error {
	projectsChan := make(chan *manifest.Project)
	var g errgroup.Group

	for range runtime.NumCPU() {
		g.Go(func() error {
			for p := range projectsChan {
				if errors.Is(ctx.Err(), context.Canceled) {
					return ctx.Err()
				}

				if err := callback(p); err != nil {
					log.Printf("error handling project %s: %v", p.Name, err)
					return err
				}
			}
			return nil
		})
	}
	go func() {
		for _, prj := range projects {
			if shouldExcludeProject(prj, groups) {
				continue
			}
			projectsChan <- prj
		}
		close(projectsChan)
	}()

	defer func() {
		// drain the channel in case of error
		for range projectsChan {
		}
	}()

	return g.Wait()
}

func splitNestedProjects(projects []*manifest.Project) (todo, nested []*manifest.Project, err error) {
	set := make(map[string]struct{}, len(projects))
	for _, p := range projects {
		if path.IsAbs(p.GetPath()) {
			return nil, nil, fmt.Errorf("path %q for project %q is absolute", p.GetPath(), p.Name)
		}
		set[p.GetPath()] = struct{}{}
	}
	for _, p := range projects {
		found := false
		dir := path.Dir(p.GetPath())
		for dir != "." {
			if _, found = set[dir]; found {
				break
			}
			dir = path.Dir(dir)
		}
		if found {
			nested = append(nested, p)
		} else {
			todo = append(todo, p)
		}
	}

	return todo, nested, nil
}

type createProjectsResult struct {
	IgnoredProjectCount          int
	SlowProjectCount             int
	ClientGeneratedDeepTreeCount int
}

func (a *ABFS) fetchABFSRefs() error {
	statusRefs, err := a.store.SearchRefs(a.ManifestServer, "", binding.ProjectStatusRef, 0)
	if err != nil {
		return err
	}
	a.statusRefs = make(map[string]entities.Hash, len(statusRefs))
	for _, ref := range statusRefs {
		a.statusRefs[ref.Project] = ref.Hash
	}

	packsRefs, err := a.store.SearchRefs(a.ManifestServer, "", binding.PacksRefs, 0)
	if err != nil {
		return err
	}
	a.packRefs = make(map[string]entities.Hash, len(packsRefs))
	for _, ref := range packsRefs {
		a.packRefs[ref.Project] = ref.Hash
	}

	return nil
}

func (a *ABFS) createProjects(ctx context.Context, root bindcas.Dir, man *manifest.Manifest, groups []string) (createProjectsResult, error) {
	projects, err := man.ApplyRemoves()
	if err != nil {
		return createProjectsResult{}, err
	}
	var errs []error
	debugChan := make(chan string)
	go func() {
		fmt.Printf("Creating projects...")
		if a.DebugConfig.Debug {
			fmt.Printf("\033[s")
		}
		activeCount := 0
		doneCount := 0
		for p := range debugChan {
			if strings.HasSuffix(p, "start") {
				activeCount++
			} else {
				doneCount++
				activeCount--
			}
			if a.DebugConfig.Debug {
				fmt.Printf("\033[u\033[K")
				fmt.Printf("A=%d D=%d %s", activeCount, doneCount, p)
			}
		}
		if a.DebugConfig.Debug {
			fmt.Printf("\033[u\033[K")
		}
		fmt.Printf(" complete\n")
	}()

	ignored := 0
	for len(projects) > 0 {
		todo, nested, err := splitNestedProjects(projects)
		if err != nil {
			return createProjectsResult{}, err
		}
		err = forEachProject(ctx, todo, groups, func(p *manifest.Project) error {
			start := time.Now()
			debugChan <- (p.Name + " start")
			err := a.createProject(ctx, a.repo.Manifest(), p, root)
			if err != nil {
				var e *errProjectNotExist
				if a.IgnoreMissingProjects && errors.As(err, &e) {
					ignored++
					// If IgnoreMissingProjects is true, we are only ignoring errors that would be caused by the projects missing.
					// Different errors could still be returned, but they are functional errors rather than errors due to
					// the projects missing in this ABFS configuration.
					log.Printf("project %q was ignored because --ignore-missing-projects option was enabled and it encountered a missing error: %v", p.Name, err)
					return nil
				}
				return err
			}
			if err := a.createSymlinks(ctx, p, root); err != nil {
				return fmt.Errorf("error while creating the manifest symlinks: %w", err)
			}
			if err := a.copyFiles(ctx, p, root); err != nil {
				return fmt.Errorf("error while creating the manifest copyfiles: %w", err)
			}
			dur := time.Since(start)
			if dur > 1*time.Second {
				a.slowProjects.Add(1)
			}
			debugChan <- fmt.Sprintf("%s complete %f seconds", p.Name, dur.Seconds())
			return err
		})
		if err != nil {
			errs = append(errs, err)
		}
		projects = nested
	}
	close(debugChan)
	slowCount := int(a.slowProjects.Load())
	deepTreeCount := int(a.clientGeneratedDeepTrees.Load())
	if len(errs) > 0 {
		return createProjectsResult{
			IgnoredProjectCount:          ignored,
			SlowProjectCount:             slowCount,
			ClientGeneratedDeepTreeCount: deepTreeCount,
		}, fmt.Errorf("errors while creating the projects: %w", errors.Join(errs...))
	}
	return createProjectsResult{
		IgnoredProjectCount:          ignored,
		SlowProjectCount:             slowCount,
		ClientGeneratedDeepTreeCount: deepTreeCount,
	}, nil
}

func (a *ABFS) createManifestProject(ctx context.Context, root bindcas.Dir) error {
	needRepoInit := a.GitRepoInit && !a.GitRepoSetupDone

	refs, err := a.store.SearchRefs(a.ManifestServer, a.ManifestProjectName, fmt.Sprintf("refs/heads/%s", a.ManifestBranch), 0)
	if err != nil {
		return err
	}
	dst := path.Join(casgit.RepoDirName, "manifests")

	// The manifest project creation is unique because it
	// is created in isolation and separate from the projects found
	// in the manifest. However, the API for many of our project related
	// methods use 'manifest' based types. We need to fake a manifest here
	// to ensure proper creation of the project.
	mProject := manifest.Project{
		Name:     a.ManifestProjectName,
		Path:     dst,
		Revision: refs[0].Hash.String(),
		Remote:   metaManifestRemote,
	}
	var mManifest manifest.Manifest
	manFetch, err := url.JoinPath(instance.MaybeAddScheme(a.ManifestServer, a.ManifestScheme))
	if err != nil {
		return err
	}
	mManifest.AddRemote(&manifest.Remote{
		Name:  metaManifestRemote,
		Fetch: manFetch,
	})

	if err := a.repo.AddURLRemoteCache(metaManifestRemote, manFetch); err != nil {
		return err
	}

	if err := a.createProject(ctx, &mManifest, &mProject, root); err != nil {
		return err
	}

	casgit.SetupCentralCommitMsgHook(ctx, a.store, a.mnt.CASFS(), a.MountPoint)

	if needRepoInit {
		// repo tools always assume remote origin exists, rename the
		// remote to make it works

		//FIXME Remote rename takes a long time to finish.
		// if err := a.shgit.RenameRemote(dst, metaManifestRemote, "origin"); err != nil {
		// 	return err
		// }
		if err := replaceInFile(path.Join(dst, ".git", "config"), metaManifestRemote, "origin"); err != nil {
			return err
		}
		if err := replaceInFile(path.Join(dst, ".git", "packed-refs"), metaManifestRemote, "origin"); err != nil {
			return err
		}

		if err := a.shgit.CheckoutNewBranch(dst, "default", fmt.Sprintf("refs/remotes/origin/%s", a.ManifestBranch)); err != nil {
			return err
		}
	}
	if err := writeManifest(ctx, root, path.Join(casgit.RepoDirName, "manifest.xml"), a.repo.Manifest()); err != nil {
		return err
	}
	if d, err := root.MkdirAll(ctx, RepoProjectsObjDir); err != nil {
		return err
	} else {
		if err := d.SetExcluded(ctx, true); err != nil {
			return err
		}
	}
	if d, err := root.MkdirAll(ctx, RepoProjectsDir); err != nil {
		return err
	} else {
		if err := d.SetExcluded(ctx, true); err != nil {
			return err
		}
	}

	if needRepoInit {
		if d, err := root.MkdirAll(ctx, RepoBinDir); err != nil {
			return err
		} else {
			if err := d.SetExcluded(ctx, true); err != nil {
				return err
			}
		}

		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if err := a.shgit.GitCloneBranchAt(ctx, RepoBinDir, "https://gerrit.googlesource.com/git-repo", "stable"); err != nil {
			log.Printf("Warning: cloning git-repo tools failed, please put your git-repo under .repo, error: %s", err.Error())
		}
	}

	return nil
}

func replaceInFile(filePath string, oldContent, newContent string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	modifiedContent := strings.ReplaceAll(string(content), oldContent, newContent)

	return os.WriteFile(filePath, []byte(modifiedContent), 0644)
}

func (a *ABFS) createManifestFiles(ctx context.Context, root bindcas.Dir, man, pinned *manifest.Manifest) error {
	start := time.Now()

	if _, err := root.MkdirAll(ctx, path.Join(casgit.RepoDirName, casgit.RepoPacksDir)); err != nil {
		return err
	}
	if err := bindcas.SetExcluded(filepath.Join(a.MountPoint, root.Path(), casgit.RepoDirName), true); err != nil {
		return err
	}

	if !a.LocalOnly {
		if err := a.fetchABFSRefs(); err != nil {
			return err
		}
	}

	pinnedManifestDir := ABFSManifestsDir
	if err := a.createManifestProject(ctx, root); err != nil {
		return err
	}
	if err := writeManifest(ctx, root, path.Join(casgit.RepoDirName, pinnedManifestDir, DefaultManifestFileName), man); err != nil {
		return err
	}

	if err := writeManifest(ctx, root, path.Join(casgit.RepoDirName, pinnedManifestDir, PinnedManifestFileName), pinned); err != nil {
		return err
	}

	res, err := a.createProjects(ctx, root, pinned, a.MountGroups)
	if err != nil {
		return err
	}

	log.Printf("The manifest files and directories created in %s", time.Since(start))
	if res.SlowProjectCount > 0 {
		log.Printf("Number of projects that took more than 1 second to create: %d", res.SlowProjectCount)
	}
	if res.ClientGeneratedDeepTreeCount > 0 {
		log.Printf("Number of client-generated deep trees: %d", res.ClientGeneratedDeepTreeCount)
	}
	if res.IgnoredProjectCount > 0 {
		log.Printf("Warning: %d projects from the manifest were ignored due to errors and the --ignore-missing-projects option was enabled.", res.IgnoredProjectCount)
	}
	if a.GitRepoInit {
		// handle chroot to have the same behavior
		a.GitRepoSetupDone = true
	}
	return nil
}

func writeManifest(ctx context.Context, root bindcas.Dir, manPath string, man *manifest.Manifest) error {
	xml, err := xml.MarshalIndent(man, "", " ")
	if err != nil {
		return err
	}
	return filesystem.WriteFile(ctx, root, manPath, bytes.NewReader(xml))
}

func (a *ABFS) configureLFS(ctx context.Context, prjPath string) error {
	pathToOID, err := a.shgit.LFSFiles(prjPath)
	if err != nil {
		return err
	}
	for lfsPath, oids := range pathToOID {
		oid := oids[0]
		lfsReq := &binding.LFSRequest{LFSOID: oid}
		if err := lfsReq.Encode(); err != nil {
			return err
		}
		hashes, err := a.store.MapResults(ctx, lfsReq.Hash)
		if err != nil {
			return err
		}
		if len(hashes) == 0 {
			if err := a.store.RefreshMapResults(ctx, lfsReq.Hash); err != nil {
				return err
			}
			hashes, err = a.store.MapResults(ctx, lfsReq.Hash)
			if err != nil {
				return err
			}
		}
		if len(hashes) != 1 {
			return fmt.Errorf("expecting a single hash for a lfsrequest, but got %v in projPath: %q for path: %q with hash: %v", len(hashes), prjPath, lfsPath, lfsReq.Hash)
		}
		result, err := binding.FetchObject[binding.LFSResult](ctx, a.store, hashes[0])
		if err != nil {
			return err
		}
		if err = bindcas.SetHash(path.Join(prjPath, lfsPath), result.ABFSOID); err != nil {
			return err
		}
		if err = a.shgit.UpdateIndex(prjPath, lfsPath); err != nil {
			return err
		}
	}

	return a.shgit.InstallLFSHooks(prjPath)
}

func (a *ABFS) configureCommitMsgHook(ctx context.Context, prjPath string) error {
	centralFile := path.Join(a.MountPoint, ".repo", "repo", "hooks", "commit-msg")

	// If the central hook file exists, create the relative symlink
	if _, err := os.Stat(centralFile); err == nil {
		targetFile := path.Join(a.MountPoint, prjPath, ".git", "hooks", "commit-msg")
		hooksDir := path.Dir(targetFile)
		if err := os.MkdirAll(hooksDir, 0755); err != nil {
			return err
		}
		rel, err := filepath.Rel(hooksDir, centralFile)
		if err != nil {
			return err
		}
		_ = os.Remove(targetFile)
		if err := os.Symlink(rel, targetFile); err != nil {
			return err
		}
		_ = bindcas.SetExcluded(targetFile, true)
	}

	return nil
}

type errProjectNotExist struct {
	Inner error
}

func (e *errProjectNotExist) Error() string {
	return e.Inner.Error()
}

func (e *errProjectNotExist) Unwrap() error {
	return e.Inner
}

func (a *ABFS) projectStatus(ctx context.Context, server string, project *manifest.Project) (*binding.ProjectStatus, error) {
	if a.LocalOnly {
		return nil, nil
	}

	if server == a.ManifestServer {
		hash := a.statusRefs[project.Name]
		return binding.FetchObject[binding.ProjectStatus](ctx, a.store, hash)
	}
	fmt.Printf("using uncached status for %q %q %q\n", server, project.Name, a.ManifestServer)

	return binding.FetchProjectStatusFromStore(ctx, server, project.Name, a.store)
}

func (a *ABFS) projectCommitPacks(ctx context.Context, server string, project *manifest.Project, hash entities.Hash) (*object.Commit, *binding.PackFiles, error) {
	commit, err := a.git.ResolveCommit(ctx, hash)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot fetch commit %s: %w", hash, err)
	}
	if a.LocalOnly {
		return commit, &binding.PackFiles{
			Version: binding.CurrentPackFilesVersion,
		}, nil
	}

	if server == a.ManifestServer {
		hash := a.packRefs[project.Name]
		packs, err := binding.FetchObject[binding.PackFiles](ctx, a.store, hash)
		if entities.IsNotExist(err) || err == nil && hash.IsZero() {
			return commit, &binding.PackFiles{
				Version: binding.CurrentPackFilesVersion,
			}, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch packs from store for server: %q project: %q err: %w", server, project.Name, err)
		}
		return commit, packs, nil
	}
	fmt.Printf("using uncached packs for %s %s\n", server, project.Name)
	packs, err := binding.FetchPacksFromStore(ctx, server, project.Name, a.store)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch packs from store for server: %q project: %q err: %w", server, project.Name, err)
	}
	return commit, packs, nil
}

func (a *ABFS) createProject(ctx context.Context, man *manifest.Manifest, project *manifest.Project, root bindcas.Dir) error {
	err := a.createProjectFromFSOperation(ctx, man, project, root)
	if err != nil {
		if a.MountFallbackToGit && !a.LocalOnly {
			log.Printf("ABFS setup for %s failed (%v), falling back to git...", project.Name, err)
			return a.checkoutFromGit(ctx, man, project, root)
		}
		if entities.IsNotExist(err) {
			return &errProjectNotExist{Inner: err}
		}
		return err
	}
	return nil
}

func (a *ABFS) createProjectFromFSOperation(ctx context.Context, man *manifest.Manifest, project *manifest.Project, root bindcas.Dir) error {
	hash := entities.NewHash(project.Revision)

	server, err := a.repo.SchemeURLForRemote(project.Remote)
	if err != nil {
		return err
	}
	server = instance.MaybeRemoveScheme(server)

	status, err := a.projectStatus(ctx, server, project)
	if err != nil {
		if !entities.IsNotExist(err) {
			return fmt.Errorf("cannot fetch project status for %q: %w", project.Name, err)
		}

		// FIXME: In a new setup ProjectStatus missing, means the project is not fully pushed yet.
		// But we still want to support the new client with old setup, so ignore the error for now.
		status = &binding.ProjectStatus{
			Version: binding.CurrentProjectStatusVersion,
		}
		// if !a.MountFallbackToGit || a.LocalOnly {
		// 	return &errProjectNotExist{
		// 		Inner: err,
		// 	}
		// }

		// return a.checkoutFromGit(ctx, project, root)
	}
	if status != nil && time.Since(status.LastSuccess) > minProjectStaleWarn {
		log.Printf("Warning: project %q is stale, last success push %s ago", project.Name, time.Since(status.LastSuccess))
	}

	commit, packs, err := a.projectCommitPacks(ctx, server, project, hash)
	if err != nil {
		return err
	}

	prjPath := project.GetPath()
	prjDir, err := root.MkdirAll(ctx, prjPath)
	if err != nil {
		return fmt.Errorf("cannot create directory for project %s: %w", prjPath, err)
	}

	packsDir := path.Join(casgit.RepoDirName, casgit.RepoPacksDir)
	// For deduplication. There was a bug causing the same packfile to be inserted with all data the same
	// besides the time.
	packHashSet := make(map[string]struct{})
	packsPatch := make(entities.Patch, 0, 2*len(packs.Packs))
	indices := make([]entities.Hash, 0, len(packs.Packs))

	for _, pack := range packs.Packs {
		if _, ok := packHashSet[pack.PackID]; ok {
			continue
		}
		packHashSet[pack.PackID] = struct{}{}
		indices = append(indices, pack.Index)
		packsPatch = append(packsPatch, &entities.PatchEntry{
			Path: path.Join(packsDir, pack.Index.String()),
			Mode: entities.FileModeRegular,
			Hash: pack.Index,
			Size: pack.IndexSize,
		}, &entities.PatchEntry{
			Path: path.Join(packsDir, pack.Content.String()),
			Mode: entities.FileModeRegular,
			Hash: pack.Content,
			Size: pack.ContentSize,
		})
	}

	if a.PrefetchGitIndices && !a.LocalOnly {
		if err := a.cacheman.Prefetch(indices, nil, gitIndicesPrefetchPriority); err != nil {
			log.Printf("cannot prefetch packs for project %s: %v", project.Name, err)
		}
	}

	if err := a.mnt.MatchAndPatch(ctx, 1, "", nil, packsPatch, nil); err != nil {
		return err
	}

	dirHash := entities.Hash(commit.TreeHash)
	for _, anno := range project.Annotation {
		if anno.Name == repo.ABFSDirHashTag {
			dirHash = entities.NewHash(anno.Value)
			break
		}
	}
	req := binding.NewDeepTreeRequest(dirHash, a.store)
	origMapFunc := req.MapFunc
	req.MapFunc = func(mo maps.MapObject) (maps.MapObject, error) {
		a.clientGeneratedDeepTrees.Add(1)
		return origMapFunc(mo)
	}

	mapObj, err := maps.Map(ctx, req)
	if err != nil {
		return fmt.Errorf("cannot create deeptree for project %s (tree=%s): %w", prjPath, dirHash, err)
	}
	deepTree := mapObj.(*gitobjects.DeepTreeObject)

	if a.PrefetchSymlinks {
		if err := binding.PrefetchSymlinks(ctx, deepTree, a.store, a.cacheman); err != nil {
			log.Printf("cannot prefetch symlinks for project %s: %v", project.Name, err)
		}
	}

	if err := prjDir.DeepUpdate(ctx, deepTree.Hash); err != nil {
		return fmt.Errorf("cannot set deep-tree %s (tree=%s): %w", deepTree.Hash, commit.TreeHash, err)
	}

	if !a.GitRepoInit || a.GitRepoSetupDone {
		err = a.mnt.NewGitDir(ctx, root, prjDir, filesystem.ProjectInfo{
			Commit:           commit,
			Git:              a.git,
			Project:          project,
			Manifest:         man,
			Server:           server,
			Packs:            packs,
			SchemeManProjURL: a.schemeManProjURL,
			PartialClone:     a.PartialClone,
		})
	} else {
		err = a.setupRepoGit(ctx, root, filesystem.ProjectInfo{
			Commit:           commit,
			Project:          project,
			Manifest:         man,
			Server:           server,
			Packs:            packs,
			SchemeManProjURL: a.schemeManProjURL,
			PartialClone:     a.PartialClone,
		})
	}

	if err != nil {
		return fmt.Errorf("cannot create git directory for project %s: %w", prjPath, err)
	}

	if a.LFS && status.LFS {
		if err := a.configureLFS(ctx, prjPath); err != nil {
			log.Printf("cannot setup LFS: %v", err)
		}
	}
	return a.configureCommitMsgHook(ctx, prjPath)
}

func (a *ABFS) newCloneOptions(project *manifest.Project, projectURL string) casgit.CloneOptions {
	prjPath := project.GetPath()
	fullPath := filepath.Join(a.MountPoint, prjPath)

	opts := casgit.CloneOptions{
		RemoteRepo: projectURL,
		Directory:  fullPath,
		Origin:     "origin",
		GitDir:     filepath.Join(fullPath, ".git"),
		ObjectsDir: filepath.Join(fullPath, ".git", "objects"),
	}
	if project.Remote != "" {
		opts.Origin = project.Remote
	}
	if entities.IsHash(project.Revision) {
		opts.Commit = entities.NewHash(project.Revision)
	} else {
		opts.Branch = project.Revision
	}

	if a.GitRepoInit {
		isRepoArtifact := path.Dir(prjPath) == casgit.RepoDirName
		var gitDir, objDir string
		if isRepoArtifact {
			gitDir = path.Join(casgit.RepoDirName, path.Base(prjPath)+".git")
			objDir = path.Join(gitDir, "objects")
		} else {
			gitDir = repoWorkTreeGitPath(project)
			objDir = path.Join(repoObjectGitPath(project), "objects")
		}

		opts.GitDir = filepath.Join(a.MountPoint, gitDir)
		opts.ObjectsDir = filepath.Join(a.MountPoint, objDir)
	}
	return opts
}

func (a *ABFS) checkoutFromGit(ctx context.Context, man *manifest.Manifest, project *manifest.Project, root bindcas.Dir) error {
	server, err := a.repo.SchemeURLForRemote(project.Remote)
	if err != nil {
		return fmt.Errorf("failed to get server url from the project: %w", err)
	}

	server = instance.MaybeAddScheme(server, "https")
	projectURL, err := url.JoinPath(server, project.Name)
	if err != nil {
		return fmt.Errorf("failed to join url path for %s and %s: %w", server, project.Name, err)
	}
	prjPath := project.GetPath()
	opts := a.newCloneOptions(project, projectURL)

	// TODO: First try to mount with ABFS with default branch of cache once ABFS can fully support
	//  Git commands without performance issue.
	fallbackBranch := ""
	if man.Default != nil && man.Default.Revision != "" && !entities.IsHash(man.Default.Revision) {
		fallbackBranch = man.Default.Revision
	}
	if fallbackBranch == "HEAD" {
		fallbackBranch = ""
	}

	client, err := binding.NewClient(opts.Directory, entities.ABFSSocketFileName, a.CacheDir, a.LocalOnly, a.FetchTimeout)
	if err == nil {
		casfs := bindcas.NewExternalCASFS(client.Root())
		err := a.tryOptimizedGitFallback(ctx, project, opts, client, casfs, fallbackBranch)
		if err := client.Close(); err != nil {
			log.Printf("failed to close client for project %s: %v", project.Name, err)
		}

		if err == nil {
			log.Printf("Optimized git fallback for %s succeeded.", project.Name)
			return nil
		}
		log.Printf("Optimized git fallback for %s failed (%v), falling back to full clone.", project.Name, err)
	}

	if err := os.RemoveAll(filepath.Join(a.MountPoint, prjPath)); err != nil {
		log.Printf("failed to remove directory for project %s: %v", project.Name, err)
	}

	log.Printf("Cloning project %s via git fallback...", project.Name)
	prjDir, err := root.MkdirAll(ctx, prjPath)
	if err != nil {
		return fmt.Errorf("cannot create directory for project %s: %w", prjPath, err)
	}
	// Exclude project and .git directory from cache so we don't upload packfiles and other git objects.
	// All files will be backed by the scratch directory of the mount.
	prjDirAbs := filepath.Join(a.MountPoint, prjPath)
	if err := bindcas.SetExcluded(prjDirAbs, true); err != nil {
		return fmt.Errorf("failed to exclude project %s from cache: %w", prjPath, err)
	}
	dotGitDir := filepath.Join(prjDirAbs, ".git")
	if err := os.MkdirAll(dotGitDir, 0755); err != nil {
		return fmt.Errorf("cannot create .git directory for project %s: %w", prjPath, err)
	}
	if err := bindcas.SetExcluded(dotGitDir, true); err != nil {
		return fmt.Errorf("failed to exclude project %s .git dir from cache: %w", prjPath, err)
	}

	err = a.shgit.GitCloneCommitAt(prjDir.Path(), projectURL, project.Remote, project.Revision)
	if err != nil {
		return fmt.Errorf("cannot checkout project %s: %w", project.Path, err)
	}
	log.Printf("Checked out %s from git.", project.Name)
	return nil
}

func (a *ABFS) createSymlinks(ctx context.Context, project *manifest.Project, root bindcas.Dir) error {
	for _, link := range project.Linkfile {
		target := path.Join(project.GetPath(), link.Src)
		_, err := bindcas.Stat(path.Join(a.MountPoint, root.Path(), target))
		if entities.IsNotExist(err) {
			continue
		}
		symlinkTarget, err := filepath.Rel(filepath.Dir(link.Dest), target)
		if err != nil {
			return fmt.Errorf("cannot create symlink %s pointing to %s", link.Dest, link.Src)
		}
		_, err = bindcas.Stat(path.Join(a.MountPoint, root.Path(), link.Dest))
		if err != nil && !entities.IsNotExist(err) {
			return err
		} else if err == nil {
			// The behavior in repo is remove the existing files and then recreate it
			if err := bindcas.Remove(path.Join(a.MountPoint, root.Path(), link.Dest)); err != nil {
				return err
			}
		}
		symlinkDir, symlinkName := path.Split(link.Dest)
		dir, err := root.MkdirAll(ctx, symlinkDir)
		if err != nil {
			return fmt.Errorf("cannot create directory for symlink %s: %w", link.Dest, err)
		}
		f, err := dir.Symlink(ctx, symlinkName, symlinkTarget)
		if err != nil {
			return fmt.Errorf("cannot create symlink %s: %w", link.Dest, err)
		}
		// Don't include file in directory hash
		if err = f.SetExcluded(ctx, true); err != nil {
			return fmt.Errorf("failed to exclude file %s: %w", link.Dest, err)
		}
	}
	return nil
}

func (a *ABFS) copyFiles(ctx context.Context, project *manifest.Project, root bindcas.Dir) error {
	for _, copyFile := range project.Copyfile {
		srcFilePath := path.Join(a.MountPoint, project.GetPath(), copyFile.Src)
		dstFilePath := path.Join(a.MountPoint, copyFile.Dest)

		// TODO: update to HashStat
		stat, err := bindcas.Stat(srcFilePath)
		if err != nil {
			return fmt.Errorf("cannot stat source file of copyfile %s: %w", srcFilePath, err)
		}

		fmode := entities.FileModeRegular
		if stat.Mode()&0111 != 0 {
			fmode = entities.FileModeExecutable
		}

		dstDir := path.Dir(copyFile.Dest)
		if _, err = root.MkdirAll(ctx, dstDir); err != nil {
			return fmt.Errorf("cannot create dir %s: %w", dstDir, err)
		}

		node, err := root.Mknod(ctx, copyFile.Dest, fmode)
		if err != nil {
			return fmt.Errorf("cannot create the target file of copyfile %s: %w", dstFilePath, err)
		}
		// Don't include file in directory hash
		if err := node.SetExcluded(ctx, true); err != nil {
			return fmt.Errorf("failed to exclude file %s: %w", dstFilePath, err)
		}
		if err = bindcas.CopyHashSize(srcFilePath, dstFilePath); err != nil {
			return fmt.Errorf("cannot copy attribute from %s to %s: %w", srcFilePath, dstFilePath, err)
		}
	}
	return nil
}

func shouldExcludeProject(p *manifest.Project, groups []string) bool {
	projectGroups := append([]string{"all"}, p.Groups...)
	if !slices.Contains(projectGroups, "notdefault") {
		projectGroups = append(projectGroups, "default")
	}
	manifestGroups := groups
	if len(manifestGroups) == 0 {
		manifestGroups = []string{"default"}
	}
	match := false
	for _, manifestGroup := range manifestGroups {
		if len(manifestGroup) == 0 {
			continue
		}
		if manifestGroup[0] == '-' && slices.Contains(projectGroups, manifestGroup[1:]) {
			match = false
		} else if slices.Contains(projectGroups, manifestGroup) {
			match = true
		}
	}
	return !match
}

func (a *ABFS) initWorktreeWithDotGit(ctx context.Context, root bindcas.Dir, projectPath, dotGitPath, commit string) error {
	projectDir, err := root.MkdirAll(ctx, projectPath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(projectDir.Path(), dotGitPath)
	if err != nil {
		return err
	}
	if sym, err := projectDir.Symlink(ctx, ".git", rel); err != nil {
		return err
	} else if err := sym.SetExcluded(ctx, true); err != nil {
		return err
	}
	dotGitParent, dotGitName := filepath.Dir(dotGitPath), filepath.Base(dotGitPath)

	if err := bindcas.CreateGitIndex(dotGitParent, dotGitName, projectDir.Path(), commit); err != nil {
		return err
	}
	return nil
}

func (a *ABFS) setupRepo() error {
	ctx := context.Background()
	a.repo = repo.New(a.store, a.git, a.ManifestServer, a.ManifestProjectName, a.ManifestScheme, a.LocalManifestDir, a.IgnoreMissingProjects)
	if err := a.repo.AddChanges(a.ChangeIDs); err != nil {
		return fmt.Errorf("AddChanges failed: %v", err)
	}

	switch {
	case len(a.ManifestBytes) > 0:
		if err := a.repo.ManifestFromBytes(a.ManifestBytes); err != nil {
			return err
		}
	case a.ManifestFile != "":
		if err := a.repo.ManifestFromFile(a.ManifestFile); err != nil {
			return err
		}
	case a.ManifestFileHash != "":
		if err := a.repo.ManifestFromBlobHash(ctx, a.ManifestFileHash); err != nil {
			return err
		}
	case a.ManifestBranch != "":
		if a.LocalOnly {
			return errors.New("branch checkout is not available in local mode")
		}
		if err := a.repo.ManifestFromBranch(ctx, a.ManifestBranch, a.ManifestProjectName, a.ManifestBranchFile); err != nil {
			return err
		}
	default:
		return errors.New("please provide a branch via --branch or a manifest file via --file")
	}

	return nil
}

// cheap version of isTTY to not pull in extra dependencies.
func isTTY() bool {
	fileInfo, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fileInfo.Mode() & os.ModeCharDevice) == os.ModeCharDevice
}

func boostMsg(s string) string {
	if !isTTY() {
		return s
	}

	return fmt.Sprintf("%s%s%s", "\033[31m", s, "\033[0m")
}

func (a *ABFS) writeConfig(dotGitRoot bindcas.Dir, info filesystem.ProjectInfo) error {
	configValue, err := filesystem.ProjectGitConfig(info)
	if err != nil {
		return err
	}
	return filesystem.WriteFile(context.Background(), dotGitRoot, "config", bytes.NewReader(configValue))

}

func (a *ABFS) setupWorkTreeRefs(ctx context.Context, dotGit bindcas.Dir, symRef bool, info filesystem.ProjectInfo) error {
	projectName := info.Project.Name
	refs := []entities.ProjectRefHash{}
	for _, ref := range []string{"refs/heads", "refs/tags"} {
		ret, err := a.store.SearchRefs(info.Server, projectName, ref, 0)
		if err != nil {
			return err
		}
		refs = append(refs, ret...)
	}
	val := []byte{}
	for _, ref := range refs {
		refName := strings.Replace(ref.Ref, "refs/heads", fmt.Sprintf("refs/remotes/%s", info.Project.Remote), 1)
		val = append(val, []byte(fmt.Sprintf("%s %s\n", ref.Hash.String(), refName))...)
	}
	if err := filesystem.WriteFile(ctx, dotGit, "packed-refs", bytes.NewReader(val)); err != nil {
		return err
	}

	// repo setup this refs intetntionally for their own usage
	repoPseudoRef := fmt.Sprintf("refs/remotes/m/%s", a.ManifestBranch)
	repoPseudoVal := fmt.Sprintf("ref: refs/remotes/%s/%s", info.Project.Remote, a.ManifestBranch)

	if !symRef {
		for _, ref := range refs {
			if filepath.Base(ref.Ref) == a.ManifestBranch {
				repoPseudoVal = ref.Hash.String()
			}
		}
	}

	dir, err := dotGit.MkdirAll(ctx, filepath.Dir(repoPseudoRef))
	if err != nil {
		return err
	}
	if err := filesystem.WriteFile(ctx, dir, filepath.Base(repoPseudoRef), bytes.NewReader([]byte(repoPseudoVal+"\n"))); err != nil {
		return err
	}

	if err := a.writeConfig(dotGit, info); err != nil {
		return err
	}
	return nil
}

func (a *ABFS) createWorkTreeGit(ctx context.Context, root bindcas.Dir, targetPath string, gitObjPath string) error {
	dotGitDir, err := root.MkdirAll(ctx, targetPath)
	if err != nil {
		return err
	}
	if err := bindcas.SetExcluded(filepath.Join(a.MountPoint, targetPath), true); err != nil {
		return fmt.Errorf("failed to exclude %s from cache: %w", targetPath, err)
	}
	linkedDir := []string{
		"hooks",
		"rr-cache",
		"objects",
	}
	for _, ldir := range linkedDir {
		value, err := filepath.Rel(dotGitDir.Path(), gitObjPath)
		if err != nil {
			return err
		}
		value = filepath.Join(value, ldir)
		if _, err := dotGitDir.Symlink(ctx, ldir, value); err != nil {
			return err
		}
	}
	return nil
}

func (a *ABFS) createObjectGit(ctx context.Context, root bindcas.Dir, targetPath string, info filesystem.ProjectInfo) error {
	parentDir, err := root.MkdirAll(ctx, filepath.Dir(targetPath))
	if err != nil {
		return err
	}
	if err := a.mnt.NewRepoObjDir(ctx, root, parentDir, filepath.Base(targetPath), info); err != nil {
		return err
	}

	return nil
}

func (a *ABFS) setupRepoGit(ctx context.Context, root bindcas.Dir, info filesystem.ProjectInfo) error {
	var workTreePath, workTreeGit, objectGit string
	isRepoArtifact := path.Dir(info.Project.GetPath()) == casgit.RepoDirName
	workTreePath = info.Project.GetPath()

	if isRepoArtifact {
		// the .git storing refs and object is located in the same place for dir used to operate repo
		workTreeGit = filepath.Join(casgit.RepoDirName, filepath.Base(info.Project.Path)+".git")
		objectGit = workTreeGit
	} else {
		workTreeGit, objectGit = repoWorkTreeGitPath(info.Project), repoObjectGitPath(info.Project)
	}

	if err := a.createObjectGit(ctx, root, objectGit, info); err != nil {
		return err
	}
	if !isRepoArtifact {
		if err := a.createWorkTreeGit(ctx, root, workTreeGit, objectGit); err != nil {
			return err
		}
	}
	workTreeGitDir, err := root.MkdirAll(ctx, workTreeGit)
	if err != nil {
		return err
	}
	if err := a.setupWorkTreeRefs(ctx, a.mnt.CreateDir(workTreeGit), isRepoArtifact, info); err != nil {
		return err
	}
	if err := a.initWorktreeWithDotGit(ctx, root, workTreePath, workTreeGitDir.Path(), info.Commit.Hash.String()); err != nil {
		return err
	}
	return nil
}

type gitCloner interface {
	Clone(ctx context.Context, opts casgit.CloneOptions) error
}

type shellGit interface {
	FetchRef(repoPath string, remote string, ref string) error
	Checkout(repoPath string, ref string) error
}

func (a *ABFS) tryOptimizedGitFallback(ctx context.Context, project *manifest.Project, opts casgit.CloneOptions, client *binding.Client, casfs bindcas.CASFS, fallbackBranch string) error {
	return tryOptimizedGitFallbackImpl(casgit.NewWithClient(client, a.cacheman), a.shgit, ctx, project, opts, client, casfs, fallbackBranch)
}

func tryOptimizedGitFallbackImpl(git gitCloner, shgit shellGit, ctx context.Context, project *manifest.Project, opts casgit.CloneOptions, client *binding.Client, casfs bindcas.CASFS, fallbackBranch string) error {
	fallbackOpts := opts
	hasFallback := false

	// Determine the branch to use for fallback.
	// 1. project.Upstream takes precedence.
	// 2. fallbackBranch (from manifest default) as a secondary option.
	// 3. If neither is specified (or is "HEAD"), use "" to let casgit resolve the default branch via HEAD symref.
	branch := fallbackBranch
	if project.Upstream != "" {
		branch = project.Upstream
	}
	if branch == "HEAD" {
		branch = ""
	}

	if project.Revision != branch {
		fallbackOpts.Commit = entities.ZeroHash
		fallbackOpts.Branch = branch
		hasFallback = true
	} else if entities.IsHash(project.Revision) {
		// If Revision is a hash and matches branch (unlikely), we still need a branch.
		fallbackOpts.Commit = entities.ZeroHash
		fallbackOpts.Branch = ""
		hasFallback = true
	}

	if !hasFallback {
		return fmt.Errorf("no fallback revision available (requested revision %s is same as fallback branch %s)", project.Revision, branch)
	}

	fallbackOpts.SingleBranch = true
	fallbackOpts.NoTags = true

	if err := git.Clone(ctx, fallbackOpts); err != nil {
		return fmt.Errorf("fallback ABFS clone failed: %w", err)
	}

	// Fetch and checkout the exact revision.
	if err := shgit.FetchRef(opts.Directory, opts.Origin, project.Revision); err != nil {
		return fmt.Errorf("git fetch failed after fallback mount: %w", err)
	}

	if err := shgit.Checkout(opts.Directory, project.Revision); err != nil {
		return fmt.Errorf("git checkout failed after fallback mount: %w", err)
	}

	return nil
}

// TODO: move these helpers under manifest project
func repoWorkTreeGitPath(proj *manifest.Project) string {
	return path.Join(RepoProjectsDir, fmt.Sprintf("%s.git", proj.GetPath()))
}

func repoObjectGitPath(proj *manifest.Project) string {
	return path.Join(RepoProjectsObjDir, fmt.Sprintf("%s.git", proj.Name))
}
