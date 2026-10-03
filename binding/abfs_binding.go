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

package binding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/store"
)

type (
	Hash           = entities.Hash
	HashStat       = entities.HashStat
	Object         = objects.Object
	JSONBaseObject = objects.JSONBaseObject
)

var (
	ObjectJSONEncode = objects.ObjectJSONEncode
	ObjectJSONDecode = objects.ObjectJSONDecode
)

const (
	// The http://localhost prefix is required for routing purposes. It will
	// be stripped off when matching routes when communicating with unix domain
	// socket.
	applyPatchURL          = "http://localhost/mnt/patch"
	hashesURL              = "http://localhost/mnt/hashes"
	hashStatsURL           = "http://localhost/mnt/hashStats"
	cloneURL               = "http://localhost/mnt/clone"
	chrootURL              = "http://localhost/chroot/"
	snapshotURL            = "http://localhost/snapshot"
	gitRepoURL             = "http://localhost/git-repo/"
	wsURL                  = "http://localhost/ws"
	providerPingURL        = "http://localhost/provider-ping"
	loadTreeDigestMapURL   = "http://localhost/mnt/load-tree-digest-map"
	addDigestMapEntriesURL = "http://localhost/mnt/add-digest-map-entries"

	CtrlDir    = ".ctrl"
	TapCtrlDir = "tap"
)

type MatchError struct {
	message string
}

func NewMatchError(format string, args ...any) *MatchError {
	return &MatchError{fmt.Sprintf(format, args...)}
}

func (e *MatchError) Error() string {
	return e.message
}

func IsMatchError(err error) bool {
	var matchErr *MatchError
	return errors.As(err, &matchErr)
}

type ChrootRequest struct {
	Overlays []string `json:"overlays"`
	Views    []string `json:"views"`
}

type Client struct {
	cli         http.Client
	root        string
	store       entities.CASStore
	cacheDir    string
	cacheClient *client.Client
}

func FindRootForSocket(root, socketName string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	for p := absRoot; ; p = filepath.Dir(p) {
		sockPath := filepath.Join(p, socketName)
		stat, err := os.Stat(sockPath)
		if err == nil { // On success
			if stat.Mode()&os.ModeSocket == 0 {
				return "", fmt.Errorf("file at '%s' is not a unix domain socket", sockPath)
			}
			return p, nil
		}
		if !entities.IsNotExist(err) {
			return "", err
		}
		if p == "/" || p == filepath.Dir(p) {
			break
		}
	}
	return "", entities.NotExistErrf("error reading the expected unix domain socket %q starting from %q", socketName, root)
}

func NewClient(rootDir, socketName string, cacheDir string, localOnly bool, fetchTimeout time.Duration) (*Client, error) {
	root, err := FindRootForSocket(rootDir, socketName)
	if err != nil {
		return nil, err
	}

	cli := http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", filepath.Join(root, socketName))
			},
		},
	}

	store, err := store.NewCASStore(cacheDir, localOnly, fetchTimeout)
	if err != nil {
		return nil, err
	}

	return &Client{
		cli:         cli,
		root:        root,
		store:       store,
		cacheDir:    cacheDir,
		cacheClient: store.CachemanClient(),
	}, nil
}

func (c *Client) Close() error {
	return c.store.Close()
}

func (c *Client) Store() entities.CASStore {
	return c.store
}
func StoreEnvVars(store entities.CASStore, envVars map[string]string) (Hash, error) {
	o := &EnvVars{
		Vars: envVars,
	}
	if err := o.Encode(); err != nil {
		return Hash{}, err
	}
	if err := store.WriteObject(context.Background(), o.Hash, bytes.NewBuffer(o.Encoded)); err != nil {
		return Hash{}, err
	}
	return o.Hash, nil
}
func (c *Client) StoreEnvVars(envVars map[string]string) (Hash, error) {
	return StoreEnvVars(c.store, envVars)
}

func (c *Client) Chroot(chroot string, overlays, views []string) error {
	chrootRequest := ChrootRequest{
		Overlays: overlays,
		Views:    views,
	}
	req, err := json.Marshal(chrootRequest)
	if err != nil {
		return err
	}
	resp, err := c.cli.Post(chrootURL+chroot, "application/json", bytes.NewBuffer(req))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create chroot: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}
	return nil
}

func (c *Client) FetchHashes(chroot string, paths []string) ([]entities.Hash, error) {
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(struct {
		Chroot string
		Paths  []string
	}{
		chroot,
		paths,
	})
	if err != nil {
		return nil, err
	}

	resp, err := c.cli.Post(hashesURL, "application/json", buf)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to fetch hashes: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}

	var hashesHex []string
	if err := json.NewDecoder(resp.Body).Decode(&hashesHex); err != nil {
		return nil, err
	}

	result := make([]entities.Hash, len(hashesHex))
	for i, hex := range hashesHex {
		result[i] = entities.NewHash(hex)
	}
	return result, nil
}

func (c *Client) FetchHashStats(chroot string, paths []string) ([]entities.HashStat, error) {
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(struct {
		Chroot string
		Paths  []string
	}{
		chroot,
		paths,
	})
	if err != nil {
		return nil, err
	}

	resp, err := c.cli.Post(hashStatsURL, "application/json", buf)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to fetch hashes: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}

	var hashStats []entities.HashStat
	if err := json.NewDecoder(resp.Body).Decode(&hashStats); err != nil {
		return nil, err
	}

	return hashStats, nil
}

func (c *Client) ApplyPatch(match, patch entities.Patch, deletes entities.DeletePatch, chroot string) error {
	req, err := json.Marshal(struct {
		Match   entities.Patch
		Patch   entities.Patch
		Deletes entities.DeletePatch
		Path    string
	}{
		Match:   match,
		Patch:   patch,
		Deletes: deletes,
		Path:    chroot,
	})
	if err != nil {
		return err
	}

	resp, err := c.cli.Post(applyPatchURL, "application/json", bytes.NewBuffer(req))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to start the record: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}

	var result string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to parse result: %s", err)
	}
	if result != "" {
		return NewMatchError("%s", result)
	}
	return nil

}

func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, snapshotURL, nil)
	if err != nil {
		return Snapshot{}, err
	}

	resp, err := c.cli.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Snapshot{}, fmt.Errorf("failed to snapshot: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}

	var result Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Snapshot{}, fmt.Errorf("failed to parse result: %s", err)
	}
	return result, nil
}

func (c *Client) RestoreSnapshot(ctx context.Context, snap Snapshot) error {
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(snap); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, snapshotURL, buf)
	if err != nil {
		return err
	}

	resp, err := c.cli.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to snapshot: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}
	return nil
}

func (c *Client) MountDetails() (*MountDetails, error) {
	buf, err := os.ReadFile(filepath.Join(c.root, ".abfs.mount-details"))
	if err != nil {
		return nil, err
	}
	md := struct {
		MountDetails
		Hash entities.Hash
	}{}
	if err := json.Unmarshal(buf, &md); err != nil {
		return nil, err
	}
	if err := md.Encode(); err != nil {
		return nil, err
	}
	if md.Hash != md.MountDetails.Hash {
		return nil, fmt.Errorf("mount details hash does not match: expected %s, got %s", md.Hash, md.MountDetails.Hash)
	}
	return &md.MountDetails, nil
}

type GitRepo struct {
	GitDir       string
	ObjectsDir   string
	HeadTreeHash entities.Hash
}

func (c *Client) AddGitRepo(workDir, gitDir, objectsDir string, treeHash entities.Hash) error {
	cleanPath := func(p string) (string, error) {
		p = filepath.Clean(p)
		if p, ok := strings.CutPrefix(p, c.root+"/"); ok {
			return p, nil
		}
		if filepath.IsAbs(p) {
			return "", fmt.Errorf("path %q is outside of the root %q", p, c.root)
		}
		return p, nil
	}
	var err error
	workDir, err = cleanPath(workDir)
	if err != nil {
		return err
	}
	gitDir, err = cleanPath(gitDir)
	if err != nil {
		return err
	}
	objectsDir, err = cleanPath(objectsDir)
	if err != nil {
		return err
	}

	buf, err := json.Marshal(GitRepo{
		GitDir:       gitDir,
		ObjectsDir:   objectsDir,
		HeadTreeHash: treeHash,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPut, gitRepoURL+workDir, bytes.NewBuffer(buf))
	if err != nil {
		return err
	}

	resp, err := c.cli.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to add git-repo: %s", string(body))
	}

	return nil
}

func (c *Client) Clone(commit, path string) error {
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(struct {
		Commit string
		Path   string
	}{
		commit,
		path,
	})
	if err != nil {
		return err
	}

	resp, err := c.cli.Post(cloneURL, "application/json", buf)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to clone: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}

	return nil
}

func (c *Client) ExecuteSupport() (bool, error) {
	return c.cacheClient.ExecuteSupport()
}

func (c *Client) Execute(evalReq entities.Hash, patches []entities.Hash, storeMapResult, prefetchInputs bool) (resp entities.Hash, err error) {
	return c.cacheClient.Execute(evalReq, patches, storeMapResult, prefetchInputs)
}

func (c *Client) UseMany(hashes []entities.Hash) error {
	return c.cacheClient.UseMany(hashes)
}

// WaitFor will block until all objects are successfully uploaded to remote, so callers should have their own timeout.
func (c *Client) WaitFor(timeout time.Duration, priority float64, objects []entities.Hash) error {
	return c.cacheClient.WaitFor(timeout, priority, objects)
}

func (c *Client) Root() string {
	return c.root
}

type PutStrategy string

const (
	PutStrategyExactMatch PutStrategy = "exact-match"
	PutStrategyLocalWin   PutStrategy = "local-wins"
	PutStrategyRemoteWin  PutStrategy = "remote-wins"
)

func (s PutStrategy) IsValid() bool {
	switch s {
	case PutStrategyExactMatch, PutStrategyLocalWin, PutStrategyRemoteWin:
		return true
	default:
		return false
	}
}

type PutWSRequest struct {
	WS       string
	IsNew    bool
	Strategy PutStrategy
}

func (c *Client) RemoveWS() error {
	req, err := http.NewRequest(http.MethodDelete, wsURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.cli.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to %s %s: %d %s %s", resp.Request.Method, resp.Request.URL, resp.StatusCode, resp.Status, string(body))
	}
	return nil
}

func (c *Client) GetWS() (*WorkspaceStatus, error) {
	resp, err := c.cli.Get(wsURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get workspace: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}

	var status WorkspaceStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) PutWS(ws string, isNew bool, strategy PutStrategy) error {
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(PutWSRequest{
		WS:       ws,
		IsNew:    isNew,
		Strategy: strategy,
	})
	if err != nil {
		return err
	}
	resp, err := c.cli.Post(wsURL, "application/json", buf)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to %s %s: %d %s %s", resp.Request.Method, resp.Request.URL, resp.StatusCode, resp.Status, string(body))
	}
	return nil
}

func (c *Client) ProviderPing() error {
	resp, err := c.cli.Post(providerPingURL, "application/json", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to ping provider: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}
	return nil
}

func (c *Client) LoadTreeDigestMap(ctx context.Context, tree entities.Hash) error {
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(struct {
		Tree string `json:"tree"`
	}{
		Tree: tree.String(),
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loadTreeDigestMapURL, buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.cli.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to load tree digest map: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}
	return nil
}

func (c *Client) AddDigestMapEntries(ctx context.Context, entries []DigestEntry) error {
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(struct {
		Entries []DigestEntry `json:"entries"`
	}{
		Entries: entries,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addDigestMapEntriesURL, buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.cli.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to add digest map entries: %d %s %s", resp.StatusCode, resp.Status, string(body))
	}
	return nil
}
