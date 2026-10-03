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

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/dagsync"
	"github.com/google/abfs/shared/entities"
	rbemodel "github.com/google/abfs/shared/rbe/model"
	"github.com/google/abfs/shared/version"
)

func (c *CacheMan) httpHandler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/ping", c.handlePing).Methods("POST")
	r.HandleFunc("/remote-ok", c.handleRemoteOk).Methods("GET", "POST")
	r.HandleFunc("/interface-version", c.handleInterfaceVersion).Methods("GET")
	r.HandleFunc("/debug", c.handleGetDebug).Methods("GET")
	r.HandleFunc("/debug", c.handlePutDebug).Methods("PUT")
	r.HandleFunc("/stats", c.handleStats).Methods("GET")
	r.HandleFunc("/log", c.handleGetLog).Methods("GET")
	r.HandleFunc("/log", c.handlePutLog).Methods("PUT")
	r.HandleFunc("/get-refs", c.handleGetRefs).Methods("POST")
	r.HandleFunc("/put-refs", c.handlePutRefs).Methods("POST")
	r.HandleFunc("/search-refs", c.handleSearchRefs).Methods("POST")
	r.HandleFunc("/sync/dump", c.handleSyncDump).Methods("GET")
	r.HandleFunc("/sync/validate", c.handleSyncValidate).Methods("GET")
	r.HandleFunc("/sync/wait", c.handleSyncWait).Methods("GET")
	r.HandleFunc("/read-index", c.handleReadIndex).Methods("GET")
	r.HandleFunc("/total-size", c.handleTotalSize).Methods("GET")
	r.HandleFunc("/lru-size", c.handleLRUSize).Methods("GET")
	r.HandleFunc("/index-put", c.handleIndexPut).Methods("POST")
	r.HandleFunc("/maintenance", c.handleMaintenance).Methods("POST")
	r.HandleFunc("/read-lru", c.handleReadLRU).Methods("GET")
	r.HandleFunc("/use-many", c.handleUseMany).Methods("POST")
	r.HandleFunc("/clean", c.handleClean).Methods("POST")
	r.HandleFunc("/new-map", c.handleNewMap).Methods("POST")
	r.HandleFunc("/fetch-map", c.handleFetchMap).Methods("POST")
	r.HandleFunc("/map-results", c.handleMapResults).Methods("POST")
	r.HandleFunc("/execute-support", c.handleExecuteSupport).Methods("GET")
	r.HandleFunc("/execute", c.handleExecute).Methods("POST")
	r.HandleFunc("/wait-for", c.handleWaitFor).Methods("POST")
	r.HandleFunc("/identity-put", c.handleIdentityPut).Methods("POST")
	r.HandleFunc("/identity-get", c.handleIdentityGet).Methods("POST")
	r.HandleFunc("/read-config", c.handleReadConfig).Methods("GET")
	r.HandleFunc("/remote-config", c.handleRemoteConfig).Methods("GET")
	r.HandleFunc("/keep-alive", c.handleKeepAlive).Methods("POST")
	r.HandleFunc("/is-object-remote", c.handleIsObjectRemote).Methods("POST")
	r.HandleFunc("/local-gc", c.handleLocalGC).Methods("POST")
	r.HandleFunc("/prefetch", c.handlePrefetch).Methods("POST")
	r.HandleFunc("/push-build-metrics", c.handlePushBuildMetrics).Methods("POST")
	return r
}

func (c *CacheMan) handlePushBuildMetrics(w http.ResponseWriter, r *http.Request) {
	var req protocol.PushBuildMetricsReq
	if !c.ReadJSON(w, r, &req) {
		return
	}

	c.buildDurationHist.Observe(req.BuildDurationSecs, req.BuildGoal, req.Status, req.AndroidTarget)

	if req.HasNinjaRun {
		c.buildCacheHitRatioHist.Observe(req.CacheHitRatio, req.BuildGoal, req.Status, req.AndroidTarget)
		c.buildCacheHitsTotal.Add(float64(req.CacheHitCount), req.BuildGoal, req.Status, req.AndroidTarget)
		c.buildCacheMissesTotal.Add(float64(req.CacheMissCount), req.BuildGoal, req.Status, req.AndroidTarget)
	}

	for phase, durationSecs := range req.Phases {
		c.buildPhaseDurationHist.Observe(durationSecs, req.BuildGoal, req.Status, req.AndroidTarget, phase)
	}

	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleIdentityPut(w http.ResponseWriter, r *http.Request) {
	if c.identityStore == nil {
		c.ErrorJSON(w, r, errors.New("identity store not enabled"), http.StatusServiceUnavailable)
		return
	}

	var req protocol.IdentityPutReq
	if !c.ReadJSON(w, r, &req) {
		return
	}

	err := c.identityStore.Put(req.SHA1, rbemodel.Node{
		Digest: rbemodel.Digest{
			SHA256: req.SHA256,
			Size:   req.Size,
		},
		Type: rbemodel.NodeType(req.Type),
	})
	if err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}

	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleIdentityGet(w http.ResponseWriter, r *http.Request) {
	if c.identityStore == nil {
		c.ErrorJSON(w, r, errors.New("identity store not enabled"), http.StatusServiceUnavailable)
		return
	}

	var req protocol.IdentityGetReq
	if !c.ReadJSON(w, r, &req) {
		return
	}

	node, found, err := c.identityStore.Get(req.SHA1)
	if err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}

	resp := protocol.IdentityGetResp{
		Found: found,
	}
	if found {
		resp.SHA256 = node.Digest.SHA256
		resp.Size = node.Digest.Size
		resp.Type = int(node.Type)
	}

	c.WriteJSON(w, r, resp)
}

func (c *CacheMan) WriteJSON(w http.ResponseWriter, r *http.Request, value any) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		c.printer.Printf("cannot encode request %q: %s\n\n", r.URL, err)
		panic(http.ErrAbortHandler)
	}
}

func (c *CacheMan) ReadJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		c.ErrorJSON(w, r, err, http.StatusBadRequest)
		return false
	}
	return true
}

func (c *CacheMan) ErrorJSON(w http.ResponseWriter, r *http.Request, err error, status int) {
	w.WriteHeader(status)
	c.WriteJSON(w, r, err.Error())
}

func (c *CacheMan) handlePing(w http.ResponseWriter, r *http.Request) {
	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleRemoteOk(w http.ResponseWriter, r *http.Request) {
	if c.abfsClient == nil {
		c.ErrorJSON(w, r, errors.New("remote client not initialized"), http.StatusServiceUnavailable)
		return
	}
	if _, ok := c.abfsClient.(*localOnlyClient); ok {
		c.ErrorJSON(w, r, errors.New("cacheman is running in local-only mode"), http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteHealthCheckPeriod)
	defer cancel()

	if err := c.checkRemoteHealth(ctx); err != nil {
		c.ErrorJSON(w, r, fmt.Errorf("remote connection unhealthy: %w", err), http.StatusServiceUnavailable)
		return
	}
	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleInterfaceVersion(w http.ResponseWriter, r *http.Request) {
	c.WriteJSON(w, r, struct {
		Implemented string `json:"implemented"`
		Required    string `json:"required"`
	}{
		Implemented: version.CachemanABFSImplementedVersion,
		Required:    version.CachemanABFSRequiredVersion,
	})
}

func (c *CacheMan) handleGetDebug(w http.ResponseWriter, r *http.Request) {
	_, _ = fmt.Fprintf(w, "%t\n", debug)
}

func (c *CacheMan) handlePutDebug(w http.ResponseWriter, r *http.Request) {
	if err := json.NewDecoder(r.Body).Decode(&debug); err != nil {
		c.ErrorJSON(w, r, err, http.StatusBadRequest)
		return
	}

	_, _ = fmt.Fprintf(w, "%t\n", debug)
}

func (c *CacheMan) handleStats(w http.ResponseWriter, r *http.Request) {
	c.WriteJSON(w, r, c.Stats())
}

func (c *CacheMan) handleGetLog(w http.ResponseWriter, r *http.Request) {
	_, _ = fmt.Fprintf(w, "%t\n", c.periodicLogging)
}

func (c *CacheMan) handlePutLog(w http.ResponseWriter, r *http.Request) {
	if err := json.NewDecoder(r.Body).Decode(&c.periodicLogging); err != nil {
		c.ErrorJSON(w, r, err, http.StatusBadRequest)
		return
	}

	_, _ = fmt.Fprintf(w, "%t\n", c.periodicLogging)
}

func (c *CacheMan) handleGetRefs(w http.ResponseWriter, r *http.Request) {
	var req protocol.GetRefsReq
	if !c.ReadJSON(w, r, &req) {
		return
	}
	if debug {
		c.printer.Printf("GetRefs(server=%q, project=%q, refs=%v)\n", req.Server, req.Project, req.Refs)
	}
	hashes, err := c.abfsClient.GetRefs(r.Context(), req.Server, req.Project, req.Refs)
	if err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}
	result := make([]string, len(hashes))
	for i, h := range hashes {
		result[i] = h.String()
	}
	c.WriteJSON(w, r, result)
}

func (c *CacheMan) handlePutRefs(w http.ResponseWriter, r *http.Request) {
	var req protocol.PutRefsReq
	if !c.ReadJSON(w, r, &req) {
		return
	}

	c.pushRefsStat.Request.Add(1)
	refHashes := make([]entities.RefHash, len(req.Refs))
	for i, ref := range req.Refs {
		refHashes[i].Hash = entities.NewHash(ref.Hash)
		refHashes[i].Ref = ref.Ref
	}

	err := c.putRefs(req.Server, req.Project, refHashes, req.Blocking)
	if err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}

	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleSearchRefs(w http.ResponseWriter, r *http.Request) {
	var req protocol.SearchRefsReq
	if !c.ReadJSON(w, r, &req) {
		return
	}

	refs, err := c.abfsClient.SearchRefs(r.Context(), req.Server, req.Project, req.RefPrefix, req.Limit)
	if err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}

	result := make([]protocol.ProjectRefHash, len(refs))
	for i, ref := range refs {
		result[i].Project = ref.Project
		result[i].Ref = ref.Ref
		result[i].Hash = ref.Hash.String()
	}
	c.WriteJSON(w, r, result)
}

func (c *CacheMan) handleSyncDump(w http.ResponseWriter, r *http.Request) {
	if c.noRemotePush() {
		c.ErrorJSON(w, r, errors.New("handleSyncDump unsupported in localOnly or remoteReadOnly mode"), http.StatusInternalServerError)
		return
	}
	sync := c.getSync()
	if sync == nil {
		c.ErrorJSON(w, r, errors.New("sync client not initialized"), http.StatusServiceUnavailable)
		return
	}
	sync.Dump(r.Context(), w)
}

func (c *CacheMan) handleSyncValidate(w http.ResponseWriter, r *http.Request) {
	if c.noRemotePush() {
		c.ErrorJSON(w, r, errors.New("handleSyncValidate unsupported in localOnly or remoteReadOnly mode"), http.StatusInternalServerError)
		return
	}
	sync := c.getSync()
	if sync == nil {
		c.ErrorJSON(w, r, errors.New("sync client not initialized"), http.StatusServiceUnavailable)
		return
	}

	timeoutStr := r.URL.Query().Get("timeout")
	timeout, err := timeoutFromStrOrDefault(timeoutStr, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sync.Validate(r.Context(), w, timeout)
}

func (c *CacheMan) handleSyncWait(w http.ResponseWriter, r *http.Request) {
	if c.noRemotePush() {
		c.ErrorJSON(w, r, errors.New("handleSyncWait unsupported in localOnly or remoteReadOnly mode"), http.StatusInternalServerError)
		return
	}
	sync := c.getSync()
	if sync == nil {
		c.ErrorJSON(w, r, errors.New("sync client not initialized"), http.StatusServiceUnavailable)
		return
	}

	timeoutStr := r.URL.Query().Get("timeout")
	timeout, err := timeoutFromStrOrDefault(timeoutStr, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = sync.Wait(timeout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestTimeout)
		return
	}

	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleReadIndex(w http.ResponseWriter, r *http.Request) {
	indexSlice, sz, err := c.index.EntriesWithTotalSize()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	c.WriteJSON(w, r, struct {
		TotalSize uint64
		Entries   []*IndexKeyEntry
	}{
		TotalSize: sz,
		Entries:   indexSlice,
	})
}

func (c *CacheMan) handleTotalSize(w http.ResponseWriter, r *http.Request) {
	c.WriteJSON(w, r, c.index.TotalSize())
}

func (c *CacheMan) handleLRUSize(w http.ResponseWriter, r *http.Request) {
	c.WriteJSON(w, r, c.index.LRUSize())
}

func (c *CacheMan) handleIndexPut(w http.ResponseWriter, r *http.Request) {
	ke := &IndexKeyEntry{}
	if err := json.NewDecoder(r.Body).Decode(ke); err != nil {
		c.ErrorJSON(w, r, err, http.StatusBadRequest)
		return
	}

	if _, _, _, _, err := c.index.Put(ke.Key, ke.IndexEntry); err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}
}

func (c *CacheMan) handleMaintenance(w http.ResponseWriter, r *http.Request) {
	if err := c.maintenanceWork(); err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}
}

func (c *CacheMan) handleReadLRU(w http.ResponseWriter, r *http.Request) {
	lru, err := c.index.ReadLRU()
	if err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}
	c.WriteJSON(w, r, lru)
}

func (c *CacheMan) handleClean(w http.ResponseWriter, r *http.Request) {
	var req protocol.CleanReq
	if !c.ReadJSON(w, r, &req) {
		return
	}

	// The intitial implementation here requires a cache lock to avoid
	// race conditions with other processes updating the cache while
	// it is being cleaned.
	if err := c.store.Lock(); err != nil {
		c.ErrorJSON(w, r, err, http.StatusBadRequest)
		return
	}
	defer func() {
		if err := c.store.Unlock(); err != nil {
			c.printer.Printf("store.Unlock failed: %v\n", err)
		}
		if err := c.store.RLock(); err != nil {
			c.printer.Printf("store.RLock failed: %v\n", err)
		}
	}()

	if err := c.clean(req); err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}

	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleNewMap(w http.ResponseWriter, r *http.Request) {
	var req protocol.NewMapMessage
	if !c.ReadJSON(w, r, &req) {
		return
	}
	c.NewMap(req.From, req.To, req.LocalOnly)
	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleFetchMap(w http.ResponseWriter, r *http.Request) {
	var from entities.Hash
	if !c.ReadJSON(w, r, &from) {
		return
	}
	wg := new(sync.WaitGroup)
	wg.Add(1)
	c.FetchMap(from, defaultFetchPriority, func(err error) {
		defer wg.Done()
		if err != nil {
			retCode := http.StatusInternalServerError
			c.printer.Printf("Failed to fetch map for %s: %v\n", from, err)
			s, ok := status.FromError(err)
			if ok && s.Code() == codes.NotFound {
				retCode = http.StatusNotFound
			}
			c.ErrorJSON(w, r, err, retCode)
		} else {
			c.WriteJSON(w, r, true)
		}
	})

	wg.Wait()
}

func (c *CacheMan) handleMapResults(w http.ResponseWriter, r *http.Request) {
	var id entities.Hash
	if err := json.NewDecoder(r.Body).Decode(&id); err != nil {
		c.ErrorJSON(w, r, err, http.StatusBadRequest)
		return
	}

	hashes, err := c.maps.Get(id)
	if err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}
	result := make([]string, len(hashes))
	for i, h := range hashes {
		result[i] = h.String()
	}
	c.WriteJSON(w, r, result)
}

func (c *CacheMan) handleExecuteSupport(w http.ResponseWriter, r *http.Request) {
	c.WriteJSON(w, r, c.aresClient.Support())
}

func (c *CacheMan) handleExecute(w http.ResponseWriter, r *http.Request) {
	var req protocol.ExecuteRequest
	if !c.ReadJSON(w, r, &req) {
		return
	}

	c.executeStat.Request.Add(1)

	resp, err := c.aresClient.Execute(r.Context(), req.EvalRequest, req.Patches, req.StoreMapResult, req.PrefetchInputs)

	if err != nil {
		c.executeStat.Failed.Add(1)
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}

	c.executeStat.Success.Add(1)
	c.WriteJSON(w, r, resp.String())
}

func (c *CacheMan) handleWaitFor(w http.ResponseWriter, r *http.Request) {
	var req protocol.WaitForRequest
	if !c.ReadJSON(w, r, &req) {
		return
	}

	timeout := time.Duration(req.TimeoutSecs * float64(time.Second))

	doneC := make(chan struct{})
	c.Phony(dagsync.RequestPriority(req.Priority), req.Objects, func(err error) {
		// Right now we are just returning success and dropping errors because we can't stream multiple results back to caller over HTTP.
		// So caller should have a timeout.
		if err != nil {
			c.printer.Printf("WaitFor http handler is waiting for success for req %s, so dropping error: %v\n", req.Objects, err)
			return
		}
		close(doneC)
	})

	if timeout > 0 {
		select {
		case <-doneC:
		case <-time.After(timeout):
			c.ErrorJSON(w, r, fmt.Errorf("wait for objects timed out after %v", timeout), http.StatusRequestTimeout)
			return
		}
	} else {
		<-doneC
	}

	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleUseMany(w http.ResponseWriter, r *http.Request) {
	var hashes []entities.Hash
	if err := json.NewDecoder(r.Body).Decode(&hashes); err != nil {
		c.ErrorJSON(w, r, err, http.StatusBadRequest)
		return
	}

	if err := c.index.UseMany(hashes); err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}

	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleReadConfig(w http.ResponseWriter, r *http.Request) {
	c.WriteJSON(w, r, c.config)
}

func (c *CacheMan) handleRemoteConfig(w http.ResponseWriter, r *http.Request) {
	c.WriteJSON(w, r, c.instance.RemoteConfig)
}

func (c *CacheMan) handleKeepAlive(w http.ResponseWriter, r *http.Request) {
	var key entities.Hash
	if !c.ReadJSON(w, r, &key) {
		return
	}

	if err := c.index.KeepAlive(key); err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}
	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handleIsObjectRemote(w http.ResponseWriter, r *http.Request) {
	var key entities.Hash
	if !c.ReadJSON(w, r, &key) {
		return
	}
	entry := c.index.Get(key)

	if entry == nil {
		ctx := context.Background()
		hdrs, err := c.abfsClient.Headers(ctx, []entities.Hash{key})
		if err != nil {
			c.WriteJSON(w, r, false)
			return
		}
		for _, hdr := range hdrs {
			if hdr.IsEmpty() {
				c.WriteJSON(w, r, false)
				return
			}
		}
		c.WriteJSON(w, r, true)
		return
	}
	c.WriteJSON(w, r, entry.Remote)
}

func (c *CacheMan) handleLocalGC(w http.ResponseWriter, r *http.Request) {
	if err := c.RunLocalGC(); err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}
	c.WriteJSON(w, r, true)
}

func (c *CacheMan) handlePrefetch(w http.ResponseWriter, r *http.Request) {
	var req protocol.PrefetchRequest
	if !c.ReadJSON(w, r, &req) {
		return
	}

	if err := c.Prefetch(r.Context(), req.Blobs, req.Objects, req.Priority); err != nil {
		c.ErrorJSON(w, r, err, http.StatusInternalServerError)
		return
	}
	c.WriteJSON(w, r, true)
}
