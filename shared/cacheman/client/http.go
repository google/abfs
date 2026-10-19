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

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"time"

	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
)

type HttpClient struct {
	http *http.Client
}

func NewHTTPClient(cacheDir string) *HttpClient {
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", path.Join(cacheDir, "http.socket"))
			},
		},
	}
	return &HttpClient{
		http: httpClient,
	}
}

// NewHTTPClientWithTimeout is like NewHTTPClient, but every request fails if
// cacheman has not answered within timeout.
func NewHTTPClientWithTimeout(cacheDir string, timeout time.Duration) *HttpClient {
	c := NewHTTPClient(cacheDir)
	c.http.Timeout = timeout
	return c
}

func (c *HttpClient) SendNewMap(from, to entities.Hash, localOnly bool) error {

	req := protocol.NewMapMessage{
		From:      from,
		To:        to,
		LocalOnly: localOnly,
	}
	return c.postJSON("/new-map", req, nil)
}

func (c *HttpClient) MapResults(from entities.Hash) ([]entities.Hash, error) {
	var hashes []string
	if err := c.postJSON("/map-results", from, &hashes); err != nil {
		return nil, err
	}

	result := make([]entities.Hash, len(hashes))
	for i, h := range hashes {
		result[i] = entities.NewHash(h)
	}
	return result, nil
}

func (c *HttpClient) FetchMapRequest(key entities.Hash) error {
	return c.postJSON("/fetch-map", key, nil)
}

func (c *HttpClient) putJSON(path string, req any) error {
	return c.doRequest("PUT", path, req, nil)
}

func (c *HttpClient) postJSON(path string, req any, result any) error {
	return c.doRequest("POST", path, req, result)
}

func (c *HttpClient) doRequest(method, path string, req any, result any) error {
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(req); err != nil {
		return err
	}

	httpReq, err := http.NewRequest(method, "http://localhost"+path, buf)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var errString string
		if err := json.Unmarshal(body, &errString); err == nil && errString != "" {
			return fmt.Errorf("failed %s %s: %d %s %s", method, path, resp.StatusCode, resp.Status, errString)
		}
		return fmt.Errorf("failed %s %s: %d %s %s", method, path, resp.StatusCode, resp.Status, string(body))
	}

	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func (c *HttpClient) getJSON(path string, result any) error {
	resp, err := c.http.Get("http://localhost" + path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var errString string
		if err := json.Unmarshal(body, &errString); err == nil && errString != "" {
			return fmt.Errorf("failed %s: %d %s %s", path, resp.StatusCode, resp.Status, errString)
		}
		return fmt.Errorf("failed %s: %d %s %s", path, resp.StatusCode, resp.Status, string(body))
	}

	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func (c *HttpClient) GetRefs(server string, project string, refs []string) ([]entities.Hash, error) {
	req := protocol.GetRefsReq{
		Server:  server,
		Project: project,
		Refs:    refs,
	}
	var hashes []string
	if err := c.postJSON("/get-refs", req, &hashes); err != nil {
		return nil, err
	}

	result := make([]entities.Hash, len(hashes))
	for i, h := range hashes {
		result[i] = entities.NewHash(h)
	}
	return result, nil
}

func (c *HttpClient) PutRefs(server string, project string, refs []entities.RefHash, blocking bool) error {
	reqRefs := make([]protocol.RefHash, len(refs))
	for i, ref := range refs {
		reqRefs[i].Ref = ref.Ref
		reqRefs[i].Hash = ref.Hash.String()
	}

	req := protocol.PutRefsReq{
		Server:   server,
		Project:  project,
		Refs:     reqRefs,
		Blocking: blocking,
	}
	var resp bool
	if err := c.postJSON("/put-refs", req, &resp); err != nil {
		return err
	}
	if !resp {
		return fmt.Errorf("expected true for put-refs request %+v", reqRefs)
	}
	return nil
}

func (c *HttpClient) UseMany(hashes []entities.Hash) error {
	var resp bool
	if err := c.postJSON("/use-many", hashes, &resp); err != nil {
		return err
	}
	if !resp {
		return fmt.Errorf("expected true for UseMany request %v", hashes)
	}
	return nil
}

func (c *HttpClient) SearchRefs(server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	req := protocol.SearchRefsReq{
		Server:    server,
		Project:   project,
		RefPrefix: refPrefix,
		Limit:     limit,
	}
	var refs []protocol.ProjectRefHash
	if err := c.postJSON("/search-refs", req, &refs); err != nil {
		return nil, err
	}

	result := make([]entities.ProjectRefHash, len(refs))
	for i, ref := range refs {
		result[i].Project = ref.Project
		result[i].Ref = ref.Ref
		result[i].Hash = entities.NewHash(ref.Hash)
	}
	return result, nil
}

func (c *HttpClient) WaitIdle(timeout time.Duration) error {
	req, err := http.NewRequest("GET", "http://localhost/sync/wait", nil)
	if err != nil {
		return err
	}

	q := req.URL.Query()
	q.Add("timeout", fmt.Sprintf("%d", int64(timeout.Seconds())))
	req.URL.RawQuery = q.Encode()

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		return errors.New(string(b))
	}

	return nil
}

func (c *HttpClient) Clean(req protocol.CleanReq) error {
	var resp bool
	if err := c.postJSON("/clean", req, &resp); err != nil {
		return err
	}
	if !resp {
		return fmt.Errorf("expected true for clean request")
	}

	return nil
}

func (c *HttpClient) getSize(path string) (uint64, error) {
	var sz uint64
	if err := c.getJSON(path, &sz); err != nil {
		return 0, err
	}
	return sz, nil
}

func (c *HttpClient) TotalSize() (uint64, error) {
	return c.getSize("/total-size")
}

func (c *HttpClient) LRUSize() (uint64, error) {
	return c.getSize("/lru-size")
}

func (c *HttpClient) Ping() error {
	return c.postJSON("/ping", nil, nil)
}

func (c *HttpClient) RemoteOK() error {
	var resp bool
	return c.getJSON("/remote-ok", &resp)
}

func (c *HttpClient) InterfaceVersion() (implemented, required string, err error) {
	var resp struct {
		Implemented string `json:"implemented"`
		Required    string `json:"required"`
	}
	if err := c.getJSON("/interface-version", &resp); err != nil {
		return "", "", err
	}
	return resp.Implemented, resp.Required, nil
}

func (c *HttpClient) ExecuteSupport() (bool, error) {
	var resp bool
	err := c.getJSON("/execute-support", &resp)

	return resp, err

}

func (c *HttpClient) SetDebug(debug bool) error {
	return c.putJSON("/debug", debug)
}

func (c *HttpClient) GetDebug() (bool, error) {
	var debug bool
	err := c.getJSON("/debug", &debug)
	return debug, err
}

func (c *HttpClient) Execute(req entities.Hash, patches []entities.Hash, storeMapResult, prefetchInputs bool) (entities.Hash, error) {
	exReq := protocol.ExecuteRequest{
		EvalRequest:    req,
		Patches:        patches,
		StoreMapResult: storeMapResult,
		PrefetchInputs: prefetchInputs,
	}
	var resp string
	if err := c.postJSON("/execute", exReq, &resp); err != nil {
		return entities.ZeroHash, err
	}
	return entities.NewHash(resp), nil
}

func (c *HttpClient) WaitFor(timeout time.Duration, priority float64, objects []entities.Hash) (err error) {
	req := protocol.WaitForRequest{
		TimeoutSecs: timeout.Seconds(),
		Priority:    priority,
		Objects:     objects,
	}
	return c.postJSON("/wait-for", req, nil)
}

func (c *HttpClient) Stats() (protocol.CacheManStats, error) {
	var stats protocol.CacheManStats
	if err := c.getJSON("/stats", &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

func (c *HttpClient) ReadConfig() (*instance.Config, error) {
	var config *instance.Config
	if err := c.getJSON("/read-config", &config); err != nil {
		return config, err
	}
	return config, nil
}

func (c *HttpClient) RemoteConfig(prevConfig *instance.ConfigOptional) (*instance.ConfigOptional, error) {
	var prevChecksum string
	if prevConfig != nil {
		prevChecksum = prevConfig.Checksum()
	}
	var config *instance.ConfigOptional
	if err := c.getJSON("/remote-config?prev-checksum="+prevChecksum, &config); err != nil {
		return nil, err
	}
	return config, nil
}

func (c *HttpClient) KeepAlive(key entities.Hash) error {
	var resp bool
	if err := c.postJSON("/keep-alive", key, &resp); err != nil {
		return err
	}
	if !resp {
		return fmt.Errorf("expected true for keep alive request")
	}
	return nil
}

func (c *HttpClient) IsObjectRemote(key entities.Hash) (bool, error) {
	var resp bool
	if err := c.postJSON("/is-object-remote", key, &resp); err != nil {
		return false, err
	}
	return resp, nil
}

func (c *HttpClient) StartGC() error {
	var resp bool
	if err := c.postJSON("/local-gc", nil, &resp); err != nil {
		return err
	}
	if !resp {
		return fmt.Errorf("expected true for local gc request")
	}
	return nil
}

func (c *HttpClient) Prefetch(blobs, objects []entities.Hash, priority float64) error {
	var resp bool
	if err := c.postJSON("/prefetch", protocol.PrefetchRequest{
		Blobs:    blobs,
		Objects:  objects,
		Priority: priority,
	}, &resp); err != nil {
		return err
	}
	if !resp {
		return fmt.Errorf("expected true for prefetch request")
	}
	return nil
}

func (c *HttpClient) PushBuildMetrics(req *protocol.PushBuildMetricsReq) error {
	var resp bool
	if err := c.postJSON("/push-build-metrics", req, &resp); err != nil {
		return err
	}
	if !resp {
		return fmt.Errorf("expected true for push-build-metrics request")
	}
	return nil
}
