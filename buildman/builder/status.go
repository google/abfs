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

package builder

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/abfs/shared/entities"
)

type ActiveBuild struct {
	RequestHash string    `json:"RequestHash"`
	StartTime   time.Time `json:"StartTime"`
}

type BuilderStatus struct {
	StartTime       time.Time     `json:"StartTime"`
	Jobs            int           `json:"Jobs"`
	ActiveJobsCount int           `json:"ActiveJobsCount"`
	TotalRequests   int64         `json:"TotalRequests"`
	SuccessRequests int64         `json:"SuccessRequests"`
	ErrorRequests   int64         `json:"ErrorRequests"`
	MemoryUsage     uint64        `json:"MemoryUsage"`
	Subscription    string        `json:"Subscription"`
	ActiveBuilds    []ActiveBuild `json:"ActiveBuilds"`
}

type StatusTracker struct {
	startTime       time.Time
	jobs            int
	subscription    string
	totalRequests   atomic.Int64
	successRequests atomic.Int64
	errorRequests   atomic.Int64

	mu           sync.RWMutex
	activeBuilds map[entities.Hash]time.Time
}

func NewStatusTracker(jobs int, subscription string) *StatusTracker {
	return &StatusTracker{
		startTime:    time.Now(),
		jobs:         jobs,
		subscription: subscription,
		activeBuilds: make(map[entities.Hash]time.Time),
	}
}

func (st *StatusTracker) BuildStarted(hash entities.Hash) {
	st.totalRequests.Add(1)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.activeBuilds[hash] = time.Now()
}

func (st *StatusTracker) BuildCompleted(hash entities.Hash, err error) {
	if err != nil {
		st.errorRequests.Add(1)
	} else {
		st.successRequests.Add(1)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.activeBuilds, hash)
}

func (st *StatusTracker) GetStatus() BuilderStatus {
	st.mu.RLock()
	activeList := make([]ActiveBuild, 0, len(st.activeBuilds))
	for h, start := range st.activeBuilds {
		activeList = append(activeList, ActiveBuild{
			RequestHash: h.String(),
			StartTime:   start,
		})
	}
	st.mu.RUnlock()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	return BuilderStatus{
		StartTime:       st.startTime,
		Jobs:            st.jobs,
		ActiveJobsCount: len(activeList),
		TotalRequests:   st.totalRequests.Load(),
		SuccessRequests: st.successRequests.Load(),
		ErrorRequests:   st.errorRequests.Load(),
		MemoryUsage:     mem.Sys,
		Subscription:    st.subscription,
		ActiveBuilds:    activeList,
	}
}

func (st *StatusTracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(st.GetStatus()); err != nil {
		log.Printf("Failed to encode /info JSON: %v", err)
		http.Error(w, fmt.Sprintf("Failed to encode JSON: %v", err), http.StatusInternalServerError)
	}
}
