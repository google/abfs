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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"time"

	"github.com/gorilla/mux"
)

type httpGitPusher struct {
	port   int
	config *GitPusherConfig
	pusher *GitPusher
	lis    net.Listener
}

func newHTTPGitPusher(port int, config *GitPusherConfig, pusher *GitPusher) (*httpGitPusher, error) {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("could not bind to port %d: %w", port, err)
	}
	return &httpGitPusher{
		port:   port,
		config: config,
		pusher: pusher,
		lis:    lis,
	}, nil
}

func (h *httpGitPusher) run() error {
	log.Printf("Starting a web-server on %s", h.lis.Addr())
	return http.Serve(h.lis, h.routes())
}

func (h *httpGitPusher) routes() *mux.Router {
	r := mux.NewRouter()
	r.PathPrefix("/debug").Handler(http.DefaultServeMux)
	r.PathPrefix("/info").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.pusher.poolConfig == nil || h.pusher.pusherConfig == nil {
			http.Error(w, "config is not loaded yet", http.StatusServiceUnavailable)
			return
		}
		stat := h.pusher.abfsClient.Stat()

		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		err := json.NewEncoder(w).Encode(struct {
			Count           int
			Index           int
			StartTime       string
			RemoteServers   []string
			Config          *GitPusherConfig
			MemoryUsage     uint64
			BytesSent       int64
			BytesReceived   int64
			MaxRequests     int
			ActiveRequests  int
			BlockedRequests int
			AllProjects     []string
			Projects        []PusherStat
			Repos           []RepoStat
		}{
			h.pusher.poolConfig.PushersCount,
			*h.pusher.pusherConfig.Index,
			h.pusher.startTime.Format(time.RFC3339),
			h.pusher.servers,
			h.config,
			mem.Sys,
			stat.BytesSent,
			stat.BytesReceived,
			h.pusher.limitedRemote.MaxConcurrent(),
			h.pusher.limitedRemote.ActiveRequests(),
			h.pusher.limitedRemote.BlockedRequests(),
			h.pusher.projectSched.Projects(),
			h.pusher.projectSched.ProjectsStats(),
			h.pusher.repoSched.ReposStats(),
		})
		if err != nil {
			log.Printf("Failed to encode JSON for /info: %v", err)
			http.Error(w, fmt.Sprintf("Failed to encode JSON: %v", err), http.StatusInternalServerError)
		}
	})
	r.HandleFunc("/sync/dump", h.handleSyncDump).Methods("GET")
	r.HandleFunc("/sync/validate", h.handleSyncValidate).Methods("GET")
	return r
}

func (h *httpGitPusher) stop() error {
	return h.lis.Close()
}

func (h *httpGitPusher) handleSyncDump(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project parameter is required", http.StatusBadRequest)
		return
	}
	if err := h.pusher.SyncDump(r.Context(), ProjectName(project), w); err != nil {
		if errors.Is(err, ErrProjectNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *httpGitPusher) handleSyncValidate(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project parameter is required", http.StatusBadRequest)
		return
	}
	timeoutStr := r.URL.Query().Get("timeout")
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		timeout = 10 * time.Second
	}
	if err := h.pusher.SyncValidate(r.Context(), ProjectName(project), w, timeout); err != nil {
		if errors.Is(err, ErrProjectNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
