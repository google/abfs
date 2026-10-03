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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/filesystem/factory"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/workspaces"
)

const (
	MountDetailsFileName    = ".abfs.mount-details"
	maxUnixSocketPathLength = 108
)

type CASFSMountOpts struct {
	Type            instance.CASFSType
	Info            filesystem.MountInfo
	MountPoint      string
	Socket          string
	MountDetails    *binding.MountDetails
	ManifestServer  string
	Cacheman        *client.Client
	Store           entities.CASStore
	MountedCallback func()
}

type casfs struct {
	filesystem.MountInfo
	mountPoint string
	store      entities.CASStore
	cacheman   *client.Client
	mnt        filesystem.Mount

	mountDetails    *binding.MountDetails
	manifestServer  string
	socket          string
	cleanups        []func()
	mountedCallback func()
	http            *filesystem.HTTP
	workspaces      *workspaces.Workspaces
	wsLock          sync.Mutex
	activeWS        *workspaces.ActiveWS
	snapshotService *SnapshotService
}

func NewCASFS(opt *CASFSMountOpts) *casfs {
	return &casfs{
		MountInfo:       opt.Info,
		mountDetails:    opt.MountDetails,
		mountPoint:      opt.MountPoint,
		mnt:             factory.NewMount(opt.Type),
		socket:          opt.Socket,
		mountedCallback: opt.MountedCallback,
		store:           opt.Store,
		manifestServer:  opt.ManifestServer,
		cacheman:        opt.Cacheman,
	}
}

func (c *casfs) startCleanupWatch(ctx context.Context) {
	go func() {
		<-ctx.Done()

		log.Printf("Stopping...")
		for i := len(c.cleanups) - 1; i >= 0; i-- {
			c.cleanups[i]()
		}
		log.Printf("...Stopping Done")
	}()
}

func (c *casfs) SetInitConfig(cfg *entities.FSConfig) {
	c.mnt.SetInitConfig(cfg)
}

func (c *casfs) Serve(ctx context.Context) error {
	c.startCleanupWatch(ctx)

	c.workspaces = workspaces.NewWorkspaces(c.store, c.cacheman)

	c.addCleanup(c.mnt.Unmount)

	if err := c.mnt.Mount(ctx, c.mountPoint, c.MountInfo, c.store); err != nil {
		return err
	}

	c.addCleanup(func() {
		c.wsLock.Lock()
		defer c.wsLock.Unlock()

		if c.activeWS != nil {
			c.activeWS.Stop()
		}
	})

	c.snapshotService = NewSnapshotService(c)

	// Create HTTP interface for mount
	c.http = filesystem.CreateHTTP(c.mnt, c.store)

	router := mux.NewRouter()

	http.HandleFunc("/", router.ServeHTTP)

	router.HandleFunc("/git-repo/{workdir:.*}", c.handlePutGitRepo).Methods("PUT")
	router.HandleFunc("/snapshot", c.handleGetSnapshot).Methods("GET")
	router.HandleFunc("/snapshot", c.handlePutSnapshot).Methods("PUT")
	router.HandleFunc("/ws", c.handleGetWorkspace).Methods("GET")
	router.HandleFunc("/ws", c.handlePostWorkspace).Methods("POST")
	router.HandleFunc("/ws", c.handleDeleteWorkspace).Methods("DELETE")
	router.PathPrefix("/mnt/").Handler(c.http.CreateRouter("/mnt"))
	router.HandleFunc("/chroot/{path:.*}", c.handleChroot).Methods("POST")
	router.HandleFunc("/provider-ping", c.handleProviderPing).Methods("POST")

	if c.socket != "" {
		go func() {
			err := c.listenUnixSocket(path.Join(c.mountPoint, c.socket))
			ctxCanceled := ctx.Err() != nil
			if !ctxCanceled && err != nil {
				log.Print(boostMsg(fmt.Sprintf("ERROR: listenUnixSocket failed: %s", err)))
			}
		}()
	}

	if err := c.putMountDetails(ctx); err != nil {
		log.Printf("Failed to put mount-details: %s", err)
	}
	if err := c.store.PutObject(ctx, binding.EmptyIndexSnapObject.Hash, binding.EmptyIndexSnapObject.Encoded); err != nil {
		log.Printf("Failed to put empty index snap: %s", err)
	}

	if c.mountedCallback != nil {
		c.mountedCallback()
	}

	fmt.Printf("Starting to serve read/write filesystem on %s\n", c.mountPoint)
	return c.mnt.Serve()
}

func (c *casfs) addCleanup(cleanup func()) {
	c.cleanups = append(c.cleanups, cleanup)
}

func (c *casfs) listenUnixSocket(p string) error {
	if len(p) >= maxUnixSocketPathLength {
		log.Print(boostMsg("WARNING: unix socket path length likely too long"))
	}
	unixListener, err := net.Listen("unix", p)
	if err != nil {
		return err
	}
	server := SelfStoppingServer{
		Server: http.Server{
			Handler: http.DefaultServeMux,
		},
		SocketPath: p,
		Done:       make(chan struct{}),
	}
	c.addCleanup(func() {
		log.Printf("Shutdown domain socket server %s...", p)
		if err := server.Shutdown(context.Background()); err != nil {
			log.Printf("Failed to shutdown domain socket server %s: %s", p, err)
		}
	})
	return server.Serve(unixListener)
}

// Wait for hash attribute available on given directory
func WaitForHash(dir string) error {
	const tries = 1000
	i := 0
	// Wait until we can read the hash value
	for ; i < tries; i++ {
		_, err := bindcas.Hash(dir)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if i == tries {
		return fmt.Errorf("cannot get the expected root hash from the CASFS")
	}

	return nil
}

// Wait for file at path to exist until specified timeout
func WaitForFile(fileName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := os.Stat(fileName)
		if err == nil {
			return nil
		}
		if !entities.IsNotExist(err) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timeout exceeded while waiting for file: %s", fileName)
}

// cheap version of isTTY to not pull in extra dependencies.
// duplicated from abfs.go to avoid a shared "utils" type package.
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

func (c *casfs) handleChroot(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	p := vars["path"]
	ctx := r.Context()

	var chrootRequest binding.ChrootRequest
	if err := json.NewDecoder(r.Body).Decode(&chrootRequest); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected request to decode into proper format: %s\n", err)
		return
	}

	if len(chrootRequest.Overlays) > 0 || len(chrootRequest.Views) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "overlays and view are not supported for casfs-mount, but received %d overlays and %d views\n", len(chrootRequest.Overlays), len(chrootRequest.Views))
		return
	}

	_, err := c.mnt.Chroot(ctx, p)
	if errors.Is(err, os.ErrExist) {
		_, _ = fmt.Fprintf(w, "chroot already exists\n")
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "error creating chroot %s\n", err)
		return
	}

	go func() {
		err := c.listenUnixSocket(path.Join(c.mountPoint, p, entities.ABFSSocketFileName))
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print(boostMsg(fmt.Sprintf("ERROR: listenUnixSocket on chroot %q failed: %s", p, err)))
		}
	}()

	_, _ = fmt.Fprintf(w, "done\n")
}

func (c *casfs) putMountDetails(ctx context.Context) error {
	if err := c.mountDetails.Encode(); err != nil {
		return fmt.Errorf("failed to encode MountDetails: %w", err)
	}
	if err := c.store.PutObject(ctx, c.mountDetails.Hash, c.mountDetails.Encoded); err != nil {
		return fmt.Errorf("failed to put MountDetails with hash %s: %w", c.mountDetails.Hash, err)
	}

	if c.mountDetails.Error == "" {
		if err := c.writeMountDetails(ctx); err != nil {
			return err
		}
	}

	if c.LocalOnly {
		return nil
	}

	if !c.LocalOnly {
		ref := fmt.Sprintf("refs/abfs/mount/%s/%d", c.mountDetails.MetaRefPrefix, c.mountDetails.StartTimestamp)
		refs := []entities.RefHash{{Ref: ref, Hash: c.mountDetails.Hash}}
		if err := c.store.PutRefs(c.manifestServer, entities.ABFSMetaProject, refs); err != nil {
			log.Printf("failed to update %s to %s: %s", ref, c.mountDetails.Hash, err)
		}
	}

	return nil
}

func (c *casfs) writeMountDetails(ctx context.Context) error {
	buf := bytes.NewBuffer(nil)
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	d := &struct {
		*binding.MountDetails
		Hash string
	}{
		c.mountDetails,
		c.mountDetails.Hash.String(),
	}
	if err := enc.Encode(d); err != nil {
		return err
	}

	if err := filesystem.WriteFile(ctx, c.mnt.Root(), MountDetailsFileName, buf); err != nil {
		return err
	}

	return c.mnt.CASFS().SetExcluded(MountDetailsFileName, true)
}

func (c *casfs) handleProviderPing(w http.ResponseWriter, r *http.Request) {
	// We never establish the 'c.cacheman' binary connection, so only check http.
	if err := c.cacheman.Ping(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "cacheman http ping failed: %s\n", err)
		return
	}
	if err := c.store.HealthCheck(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "store health check failed: %s\n", err)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "OK\n")
}
