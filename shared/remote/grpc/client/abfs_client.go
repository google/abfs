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
	"errors"
	"time"

	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/servers/auth"
)

type Hash = entities.Hash

const clientVersion = "0.1.3"
const serverMinVersion = "0.1.1"

const (
	// TODO: reduce timeout values after measuring actual times
	rpcCallTimeout = 1 * time.Hour
)

// ABFSClient serves the purpose of converting from gRPC conventions to golang conventions
type ABFSClient struct {
	cm *connectionManager
}

var _ remote.RemoteProvider = (*ABFSClient)(nil)

type ABFSClientOpts struct {
	Servers         []string
	AuthInfo        instance.AuthInfo
	SkipHealthCheck bool
}

func NewABFSClient(o *ABFSClientOpts) (*ABFSClient, error) {
	if len(o.Servers) == 0 {
		return nil, errors.New("empty server list provided")
	}
	tokenSource, err := auth.GenerateTokenSource(o.AuthInfo)
	if err != nil {
		return nil, err
	}
	cm, err := newConnectionManager(o.Servers, tokenSource, o.AuthInfo.RootCAFile, o.AuthInfo.DisableTLS, o.SkipHealthCheck)
	if err != nil {
		return nil, err
	}

	c := &ABFSClient{
		cm: cm,
	}

	return c, nil
}

func (c *ABFSClient) Close() error {
	return c.cm.close()
}

func (c *ABFSClient) HealthCheck(ctx context.Context, service string) error {
	return c.cm.healthCheck(service)
}

// Headers implements remote.RemoteProvider.
func (c *ABFSClient) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	return c.cm.get(entities.ZeroHash).Headers(ctx, keys)
}

// SHA256 implements remote.RemoteProvider.
func (c *ABFSClient) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	return c.cm.get(entities.ZeroHash).SHA256(ctx, keys)
}

// GetObject implements remote.RemoteProvider.
func (c *ABFSClient) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)

	newReader, err := c.cm.get(key).GetObject(ctx, key)
	if err != nil {
		cancel()
		return nil, err
	}

	return remote.NewContentReaderWithCustomClose(newReader, func() error {
		defer cancel()
		return newReader.Close()
	}), nil
}

// PutObject implements remote.RemoteProvider.
func (c *ABFSClient) PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	return c.cm.get(key).PutObject(ctx, key, reader)
}

func (c *ABFSClient) AddMapResult(ctx context.Context, from, to Hash) error {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	return c.cm.get(from).AddMapResult(ctx, from, to)
}

func (c *ABFSClient) MapResults(ctx context.Context, id Hash) ([]Hash, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	return c.cm.get(id).MapResults(ctx, id)
}

// GetRefs implements remote.RemoteProvider.
func (c *ABFSClient) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	return c.cm.getRefs().GetRefs(ctx, instance.MaybeRemoveScheme(server), project, refs)
}

// PutRefs implements remote.RemoteProvider.
func (c *ABFSClient) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	return c.cm.getRefs().PutRefs(ctx, instance.MaybeRemoveScheme(server), project, refs)
}

// SearchRefs implements remote.RemoteProvider.
func (c *ABFSClient) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	return c.cm.getRefs().SearchRefs(ctx, instance.MaybeRemoveScheme(server), project, refPrefix, limit)
}

func (c *ABFSClient) Mark(ctx context.Context, hash Hash, key, value string) (int64, error) {
	return 0, errors.New("not implemented")
}

// Version implements remote.RemoteProvider.
func (c *ABFSClient) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	return c.cm.get(entities.ZeroHash).Version(ctx)
}

// WhoAmI implements remote.RemoteProvider.
func (c *ABFSClient) WhoAmI(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcCallTimeout)
	defer cancel()

	return c.cm.get(entities.ZeroHash).WhoAmI(ctx)
}

func (c *ABFSClient) Stat() protocol.ClientStat {
	return protocol.ClientStat{
		BytesSent:     c.cm.bytesSent.Load(),
		LastSent:      time.Unix(0, c.cm.lastSent.Load()),
		BytesReceived: c.cm.bytesReceived.Load(),
		LastReceived:  time.Unix(0, c.cm.lastReceived.Load()),
	}
}
