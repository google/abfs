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
	"context"
	"fmt"
	"io"
	"net"
	"path"
	"time"

	"github.com/google/abfs/cas/cas/providers/cacheman"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/objects/gitobjects"
)

type Client struct {
	client     *cacheman.Client
	httpClient *HttpClient
	socketPath string
}

var _ protocol.HTTP = (*Client)(nil)
var _ protocol.CASCacheman = (*Client)(nil)

func NewClient(cacheDir string, fetchTimeout time.Duration) *Client {
	socketPath := path.Join(cacheDir, "socket")
	return &Client{
		client:     cacheman.NewClient(socketPath, fetchTimeout),
		httpClient: NewHTTPClient(cacheDir),
		socketPath: socketPath,
	}
}

func (c *Client) Serve(ctx context.Context) error {
	return c.client.Serve(ctx)
}

func (c *Client) Close() error {
	return c.client.Close()
}
func (c *Client) CasClient() *cacheman.Client {
	return c.client
}

func (c *Client) SendNewObject(key entities.Hash, objType cacheman.RequestObjectType) error {
	return c.client.SendNewObject(bindcas.CASHash(&key), objType)
}

type WriterFunc func(w io.Writer) error

func (c *Client) FetchGitObjectRequest(key entities.Hash) (cacheman.ResultType, error) {
	return c.client.FetchGitObjectRequest(bindcas.CASHash(&key))
}

func (c *Client) FetchStatRequest(key entities.Hash) (entities.StatInfo, error) {
	si, err := c.client.FetchStatRequest(bindcas.CASHash(&key))
	var out entities.StatInfo
	if err != nil {
		return out, err
	}
	var t entities.ObjectType
	switch si.GitType {
	case cacheman.BinaryBlob:
		t = entities.ObjectTypeBlob
	case cacheman.BinaryObject:
		t = gitobjects.ObjectTypeDir
	default:
		return out, fmt.Errorf("unknown object type 0x%02x", byte(si.GitType))
	}

	out = entities.StatInfo{Size: si.Size,
		Mode:    entities.FileMode(si.Mode),
		GitType: t}

	return out, nil
}

func (c *Client) FetchObjectRequest(key entities.Hash, objType cacheman.RequestObjectType) (cacheman.ResultType, error) {
	return c.client.FetchObjectRequest(bindcas.CASHash(&key), objType)
}

func (c *Client) SendNotifyUse(key entities.Hash) error {
	return c.client.SendNotifyUse(bindcas.CASHash(&key))
}

func (c *Client) SendNewMap(from, to entities.Hash, localOnly bool) error {
	if c.client.IsClosing() {
		return cacheman.ErrClosedClient
	}
	return c.httpClient.SendNewMap(from, to, localOnly)
}

func (c *Client) MapResults(from entities.Hash) ([]entities.Hash, error) {
	if c.client.IsClosing() {
		return nil, cacheman.ErrClosedClient
	}
	return c.httpClient.MapResults(from)
}
func (c *Client) FetchMapRequest(key entities.Hash) error {
	if c.client.IsClosing() {
		return cacheman.ErrClosedClient
	}
	return c.httpClient.FetchMapRequest(key)
}

func (c *Client) GetRefs(server string, project string, refs []string) ([]entities.Hash, error) {
	return c.httpClient.GetRefs(server, project, refs)
}

func (c *Client) PutRefs(server string, project string, refs []entities.RefHash, blocking bool) error {
	return c.httpClient.PutRefs(server, project, refs, blocking)
}

func (c *Client) UseMany(hashes []entities.Hash) error {
	return c.httpClient.UseMany(hashes)
}

func (c *Client) SearchRefs(server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	return c.httpClient.SearchRefs(server, project, refPrefix, limit)
}

func (c *Client) WaitIdle(timeout time.Duration) error {
	return c.httpClient.WaitIdle(timeout)
}

func (c *Client) SetDebug(debug bool) error {
	return c.httpClient.SetDebug(debug)
}

func (c *Client) GetDebug() (bool, error) {
	return c.httpClient.GetDebug()
}

func (c *Client) Clean(req protocol.CleanReq) error {
	return c.httpClient.Clean(req)
}

func (c *Client) TotalSize() (uint64, error) {
	if c.client.IsClosing() {
		return 0, cacheman.ErrClosedClient
	}

	return c.httpClient.TotalSize()
}

func (c *Client) LRUSize() (uint64, error) {
	if c.client.IsClosing() {
		return 0, cacheman.ErrClosedClient
	}

	return c.httpClient.LRUSize()
}

func (c *Client) Ping() error {
	if c.client.IsClosing() {
		return cacheman.ErrClosedClient
	}
	return c.httpClient.Ping()
}

func (c *Client) RemoteOK() error {
	if c.client.IsClosing() {
		return cacheman.ErrClosedClient
	}
	return c.httpClient.RemoteOK()
}

func (c *Client) PingBinary() error {
	if c.client.IsClosing() {
		return cacheman.ErrClosedClient
	}

	// The result of this is don't care. We just want it to return without error
	_, err := c.FetchObjectRequest(entities.ZeroHash, cacheman.TypeBlob)
	if err != nil {
		return err
	}

	return nil
}

func (c *Client) ExecuteSupport() (bool, error) {
	if c.client.IsClosing() {
		return false, cacheman.ErrClosedClient
	}
	return c.httpClient.ExecuteSupport()
}

func (c *Client) Execute(req entities.Hash, patches []entities.Hash, storeMapResult, prefetchInputs bool) (entities.Hash, error) {
	if c.client.IsClosing() {
		return entities.ZeroHash, cacheman.ErrClosedClient
	}

	return c.httpClient.Execute(req, patches, storeMapResult, prefetchInputs)
}

func (c *Client) WaitFor(timeout time.Duration, priority float64, objects []entities.Hash) error {
	if c.client.IsClosing() {
		return cacheman.ErrClosedClient
	}

	return c.httpClient.WaitFor(timeout, priority, objects)
}

func (c *Client) Stats() (protocol.CacheManStats, error) {
	if c.client.IsClosing() {
		return protocol.CacheManStats{}, cacheman.ErrClosedClient
	}

	return c.httpClient.Stats()
}

func (c *Client) ReadConfig() (*instance.Config, error) {
	return c.httpClient.ReadConfig()
}

func (c *Client) RemoteConfig(prevConfig *instance.ConfigOptional) (*instance.ConfigOptional, error) {
	return c.httpClient.RemoteConfig(prevConfig)
}

func (c *Client) KeepAlive(key entities.Hash) error {
	if c.client.IsClosing() {
		return cacheman.ErrClosedClient
	}

	return c.httpClient.KeepAlive(key)
}

func (c *Client) IsObjectRemote(key entities.Hash) (bool, error) {
	if c.client.IsClosing() {
		return false, cacheman.ErrClosedClient
	}
	return c.httpClient.IsObjectRemote(key)
}

func (c *Client) StartGC() error {
	if c.client.IsClosing() {
		return cacheman.ErrClosedClient
	}
	return c.httpClient.StartGC()
}

func (c *Client) Prefetch(blobs, objects []entities.Hash, priority float64) error {
	if len(blobs) == 0 && len(objects) == 0 {
		return nil
	}
	if c.client.IsClosing() {
		return cacheman.ErrClosedClient
	}
	return c.httpClient.Prefetch(blobs, objects, priority)
}

func (c *Client) PushBuildMetrics(req *protocol.PushBuildMetricsReq) error {
	if c.client.IsClosing() {
		return cacheman.ErrClosedClient
	}
	return c.httpClient.PushBuildMetrics(req)
}

func (c *Client) CheckStatusRequest() (protocol.ResultType, int32, error) {
	if c.client.IsClosing() {
		return protocol.ResultFailed, 0, cacheman.ErrClosedClient
	}

	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return protocol.ResultFailed, 0, err
	}
	defer conn.Close()

	if err := protocol.WriteCheckStatusReq(conn, 1); err != nil {
		return protocol.ResultFailed, 0, err
	}

	resp, err := protocol.ReadCheckStatusResp(conn)
	if err != nil {
		return protocol.ResultFailed, 0, err
	}
	return resp.Result, resp.ErrorCode, nil
}
