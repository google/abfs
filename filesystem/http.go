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

package filesystem

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gorilla/mux"
	"golang.org/x/sync/semaphore"

	"github.com/google/abfs/cas/hash/hashers/sha1"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/maps"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
)

// The default value for maximum number of os-threads in golang is 10000.
// We save 2000 for other potential concurrent sys-calls, and leave 8000 for abfs interactions.
const MaxConcurrentOps = 8000

// When to start throttling client goroutines
const ClientThrottleLevel = 10

// HTTP interface to mounts (abfs/casfs)
type HTTP struct {
	mnt      HTTPMount
	store    entities.CASStore
	git      *casgit.CASGit
	sema     *semaphore.Weighted
	reqCount atomic.Int32
}

func CreateHTTP(mnt HTTPMount, store entities.CASStore) *HTTP {
	return &HTTP{mnt: mnt, store: store, git: casgit.New(store),
		sema: semaphore.NewWeighted(MaxConcurrentOps),
	}
}
func (a *HTTP) CreateRouter(prefix string) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc(prefix+"/patch", a.handlePatch).Methods("POST")
	r.HandleFunc(prefix+"/match", a.handleMatch).Methods("POST")
	r.HandleFunc(prefix+"/hashes", a.handleHashes).Methods("POST")
	r.HandleFunc(prefix+"/hashStats", a.handleHashStats).Methods("POST")
	r.HandleFunc(prefix+"/clone", a.handleClone).Methods("POST")
	r.HandleFunc(prefix+"/set-rbe-digest", a.handleSetRBEDigest).Methods("POST")
	r.HandleFunc(prefix+"/get-rbe-digest", a.handleGetRBEDigest).Methods("POST", "GET")
	r.HandleFunc(prefix+"/set-rbe-digests", a.handleSetRBEDigests).Methods("POST")
	r.HandleFunc(prefix+"/get-rbe-digests", a.handleGetRBEDigests).Methods("POST", "GET")
	r.HandleFunc(prefix+"/get-rbe-tap-digest", a.handleGetRBETapDigest).Methods("POST")
	r.HandleFunc(prefix+"/load-tree-digest-map", a.handleLoadTreeDigestMap).Methods("POST")
	r.HandleFunc(prefix+"/add-digest-map-entries", a.handleAddDigestMapEntries).Methods("POST")

	return r
}
func WriteJson(w http.ResponseWriter, r interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(r); err != nil {
		log.Printf("Failed to write json response: %s", err)
	}
}

type PatchInput struct {
	Match   entities.Patch
	Patch   entities.Patch
	Deletes entities.DeletePatch
	Path    string
}

func (a *HTTP) getGoRoutineCount() int64 {
	count := a.reqCount.Add(1)
	if count < ClientThrottleLevel {
		// Have not seen any improvements adding more goroutines
		return 1
		//		return int64(runtime.NumCPU())
	}
	return 1
}
func (a *HTTP) handlePatch(w http.ResponseWriter, r *http.Request) {
	var input PatchInput

	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		inputFormat, _ := json.Marshal(input)
		_, _ = fmt.Fprintf(w, "expected to receive a request body that matches this struct format: %s\n", inputFormat)
		return
	}
	if strings.HasPrefix(input.Path, "./") || strings.HasPrefix(input.Path, "../") {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "the path received was '%s', but we need a valid path that doesn't start with './' or '../'\n", input.Path)
		return
	}
	result := ""
	if err := a.mnt.MatchAndPatch(context.Background(), goCount, input.Path, input.Match, input.Patch, input.Deletes); err != nil {
		if !IsMatchError(err) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, "error when applying the patch: %s\n", err)
			return
		}
		result = err.Error()
	}
	WriteJson(w, result)
}

type MatchInput struct {
	Hash string
	Path string
}

func (a *HTTP) handleMatch(w http.ResponseWriter, r *http.Request) {
	var input MatchInput
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		inputFormat, _ := json.Marshal(input)
		_, _ = fmt.Fprintf(w, "expected to receive a request body that matches this struct format: %s\n", inputFormat)
		return
	}

	if strings.HasPrefix(input.Path, "./") || strings.HasPrefix(input.Path, "../") {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "the path received was '%s', but we need a valid path that doesn't start with './' or '../'\n", input.Path)
		return
	}

	if !entities.IsHash(input.Hash) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "the hash received was '%s', but we need a valid hash in the form of '%s' (without the single quotes)\n", input.Hash, entities.EmptyDirHash)
		return
	}

	hash := entities.NewHash(input.Hash)

	result := ""
	if err := a.mnt.Matches(context.Background(), goCount, input.Path, entities.Patch{{Path: ".", Mode: entities.FileModeDir, Hash: hash}}); err != nil {
		if !IsMatchError(err) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, "error when matching: %s\n", err)
			return
		}
		result = err.Error()
	}
	WriteJson(w, result)
}

func (a *HTTP) handleHashes(w http.ResponseWriter, r *http.Request) {
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	req := new(struct {
		Chroot string
		Paths  []string
	})
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected to receive a json request body of list of paths: %s\n", err)
		return
	}

	if req.Chroot == "" {
		req.Chroot = "."
	}

	result, errs := a.mnt.Hashes(r.Context(), goCount, req.Chroot, req.Paths)
	if len(errs) > 0 {
		w.WriteHeader(http.StatusInternalServerError)
		WriteJson(w, errs)
		return
	}

	WriteJson(w, result)
}

func (a *HTTP) handleHashStats(w http.ResponseWriter, r *http.Request) {
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	req := new(struct {
		Chroot string
		Paths  []string
	})
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected to receive a json request body of list of paths: %s\n", err)
		return
	}

	if req.Chroot == "" {
		req.Chroot = "."
	}

	result, errs := a.mnt.HashStats(r.Context(), goCount, req.Chroot, req.Paths)
	if len(errs) > 0 {
		w.WriteHeader(http.StatusInternalServerError)
		WriteJson(w, errs)
		return
	}

	WriteJson(w, result)
}

func (a *HTTP) handleClone(w http.ResponseWriter, r *http.Request) {
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	req := new(struct {
		Commit string
		Path   string
	})
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected to receive a json request containing a path and a commit: %s\n", err)
		return
	}

	result := ""
	if err := a.createProject(req.Commit, req.Path); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		WriteJson(w, err.Error())
		return
	}

	WriteJson(w, result)
}

func (a *HTTP) createProject(revision, path string) error {
	ctx := context.Background()
	hash := entities.NewHash(revision)

	commit, err := a.git.GetCommit(ctx, hash)
	if err != nil {
		return fmt.Errorf("cannot fetch commit %s: %w", hash, err)
	}

	prjPath := path
	prjDir, err := a.mnt.Root().MkdirAll(ctx, prjPath)
	if err != nil {
		return fmt.Errorf("cannot create directory for project %s: %w", prjPath, err)
	}

	mapObj, err := maps.Map(context.Background(), binding.NewDeepTreeRequest(entities.Hash(commit.TreeHash), a.store))
	if err != nil {
		return fmt.Errorf("cannot create deeptree for project %s (tree=%s): %w", prjPath, commit.TreeHash, err)
	}
	deepTree := mapObj.(*gitobjects.DeepTreeObject)

	if err := prjDir.DeepUpdate(ctx, deepTree.Hash); err != nil {
		return fmt.Errorf("cannot set deep-tree %s (tree=%s): %w", deepTree.Hash, commit.TreeHash, err)
	}

	err = a.mnt.NewGitDir(ctx, a.mnt.Root(), prjDir, ProjectInfo{
		Commit:   commit,
		Git:      a.git,
		Project:  nil,
		Manifest: nil,
		Packs:    nil,
	})
	if err != nil {
		return fmt.Errorf("cannot create git directory for project %s: %w", prjPath, err)
	}
	return err
}

type SetRBEDigestsInput struct {
	Digests []RBEPathStat `json:"digests"`
}

type GetRBEDigestInput struct {
	Path string `json:"path"`
}

type GetRBEDigestsInput struct {
	Paths []string `json:"paths"`
}

func (a *HTTP) handleSetRBEDigest(w http.ResponseWriter, r *http.Request) {
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	var input RBEPathStat
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected json request body for SetRBEDigest: %s\n", err)
		return
	}

	p := input.Path
	if p == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "path is required\n")
		return
	}

	if input.SHA256 == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "sha256 is required\n")
		return
	}

	if err := a.mnt.SetRBEDigest(r.Context(), p, input.Digest, input.Mode); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "error setting digest: %s\n", err)
		return
	}
}

func (a *HTTP) handleGetRBEDigest(w http.ResponseWriter, r *http.Request) {
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	var p string
	if r.Method == http.MethodPost && r.Body != nil {
		var input GetRBEDigestInput
		_ = json.NewDecoder(r.Body).Decode(&input)
		p = input.Path
	}
	if p == "" {
		p = r.URL.Query().Get("path")
	}

	if p == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "path is required\n")
		return
	}

	resp, err := a.mnt.GetRBEDigest(r.Context(), p)
	if err != nil {
		if errors.Is(err, entities.ErrEntryNotFound) || entities.IsNotExist(err) || strings.Contains(err.Error(), "not found") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, "digest not found for path %s: %s\n", p, err)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "error getting digest: %s\n", err)
		return
	}

	WriteJson(w, resp)
}

func (a *HTTP) handleSetRBEDigests(w http.ResponseWriter, r *http.Request) {
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	var input SetRBEDigestsInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected json request body for SetRBEDigests: %s\n", err)
		return
	}

	if len(input.Digests) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "digests list cannot be empty\n")
		return
	}

	errs := a.mnt.SetRBEDigests(r.Context(), goCount, input.Digests)
	if len(errs) > 0 {
		w.WriteHeader(http.StatusInternalServerError)
		WriteJson(w, errs)
		return
	}
}

func (a *HTTP) handleGetRBEDigests(w http.ResponseWriter, r *http.Request) {
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	var paths []string
	if r.Method == http.MethodPost && r.Body != nil {
		var input GetRBEDigestsInput
		if err := json.NewDecoder(r.Body).Decode(&input); err == nil {
			paths = input.Paths
		}
	}

	if len(paths) == 0 {
		// Try query parameters
		if qPaths, ok := r.URL.Query()["path"]; ok && len(qPaths) > 0 {
			paths = qPaths
		} else if qPathsStr := r.URL.Query().Get("paths"); qPathsStr != "" {
			paths = strings.Split(qPathsStr, ",")
		}
	}

	if len(paths) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected json request body or query parameter with list of paths for GetRBEDigests\n")
		return
	}

	result, errs := a.mnt.GetRBEDigests(r.Context(), goCount, paths)
	if len(errs) > 0 {
		w.WriteHeader(http.StatusInternalServerError)
		WriteJson(w, errs)
		return
	}

	WriteJson(w, result)
}

type BackendStatDTO struct {
	IsARI bool        `json:"isARI"`
	ARI   string      `json:"ari"`
	Hash  string      `json:"hash"`
	Mode  fs.FileMode `json:"mode"`
	Size  int64       `json:"size"`
}

func backendStatsToDTOs(stats map[string]bindcas.CASBackendStat) map[string]BackendStatDTO {
	dtos := make(map[string]BackendStatDTO, len(stats))
	for path, bStat := range stats {
		hashStr := ""
		if bStat.Hash != nil {
			hashStr = bStat.Hash.String()
		}
		dtos[path] = BackendStatDTO{
			IsARI: bStat.IsARI,
			ARI:   bStat.ARI,
			Hash:  hashStr,
			Mode:  bStat.Mode,
			Size:  bStat.Size,
		}
	}
	return dtos
}

func dtosToBackendStats(dtos map[string]BackendStatDTO, opType string) (map[string]bindcas.CASBackendStat, error) {
	stats := make(map[string]bindcas.CASBackendStat, len(dtos))
	for path, dto := range dtos {
		bStat := bindcas.CASBackendStat{
			IsARI: dto.IsARI,
			ARI:   dto.ARI,
			Mode:  dto.Mode,
			Size:  dto.Size,
		}
		if dto.Hash != "" {
			hashBytes, err := hex.DecodeString(dto.Hash)
			if err != nil {
				return nil, fmt.Errorf("invalid hash hex for %s path %s: %w", opType, path, err)
			}
			var sha1Hash sha1.Sha1Hash
			copy(sha1Hash[:], hashBytes)
			bStat.Hash = &sha1Hash
		}
		stats[path] = bStat
	}
	return stats, nil
}

// MarshalJSON serializes RBETapDigestInput for client HTTP requests (e.g., in cmd/rbetap).
func (input RBETapDigestInput) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Reads  map[string]BackendStatDTO `json:"reads"`
		Writes map[string]BackendStatDTO `json:"writes"`
	}{
		Reads:  backendStatsToDTOs(input.Reads),
		Writes: backendStatsToDTOs(input.Writes),
	})
}

// UnmarshalJSON deserializes RBETapDigestInput for server HTTP handlers (e.g., in handleGetRBETapDigest).
func (input *RBETapDigestInput) UnmarshalJSON(data []byte) error {
	var raw struct {
		Reads  map[string]BackendStatDTO `json:"reads"`
		Writes map[string]BackendStatDTO `json:"writes"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var err error
	if input.Reads, err = dtosToBackendStats(raw.Reads, "read"); err != nil {
		return err
	}
	if input.Writes, err = dtosToBackendStats(raw.Writes, "write"); err != nil {
		return err
	}

	return nil
}

func (a *HTTP) handleGetRBETapDigest(w http.ResponseWriter, r *http.Request) {
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	var input RBETapDigestInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected json request body for GetRBETapDigest: %s\n", err)
		return
	}
	resp, err := a.mnt.GetRBETapDigest(r.Context(), goCount, input)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "error resolving RBT tap digest: %s\n", err)
		return
	}

	WriteJson(w, resp)
}

type LoadTreeDigestMapInput struct {
	Tree string `json:"tree"`
}

type AddDigestMapEntriesInput struct {
	Entries []binding.DigestEntry `json:"entries"`
}

func (a *HTTP) handleLoadTreeDigestMap(w http.ResponseWriter, r *http.Request) {
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	var input LoadTreeDigestMapInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected json request body for LoadTreeDigestMap: %s\n", err)
		return
	}

	if input.Tree == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "tree is required\n")
		return
	}

	treeHash := entities.NewHash(input.Tree)
	if err := a.mnt.LoadTreeDigestMap(r.Context(), treeHash); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "failed to load tree digest map: %s\n", err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (a *HTTP) handleAddDigestMapEntries(w http.ResponseWriter, r *http.Request) {
	goCount := a.getGoRoutineCount()
	defer a.reqCount.Add(-1)
	if err := a.sema.Acquire(r.Context(), goCount); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "Failed to lock resources")
		return
	}
	defer a.sema.Release(goCount)

	var input AddDigestMapEntriesInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "expected json request body for AddDigestMapEntries: %s\n", err)
		return
	}

	a.mnt.AddDigestMapEntries(input.Entries)
	w.WriteHeader(http.StatusOK)
}
