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
	"sync"
	"time"

	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
)

// fetchTypeMap is the type label of map result fetches.
const fetchTypeMap = "map"

var fetchDurationBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60, 120, 300}

// fetchTypeLabel returns the type label of an object fetch.
func fetchTypeLabel(t protocol.RequestObjectType) string {
	switch t {
	case protocol.TypeBlob, protocol.TypeTree, protocol.TypeObject, protocol.TypeARI, protocol.TypeCASBlob:
		return t.String()
	default:
		return "other"
	}
}

// fetchResultLabel returns the result label of a fetch, matching the result cacheman sends
// to the requester.
func fetchResultLabel(err error) string {
	switch {
	case err == nil:
		return "ok"
	case entities.IsNotExist(err):
		return "not_found"
	default:
		return "error"
	}
}

// inFlightRPCs keeps the start time of every RPC that is open to the server. The zero value
// is ready to use.
type inFlightRPCs struct {
	mu     sync.Mutex
	nextID uint64
	starts map[uint64]time.Time
}

func (r *inFlightRPCs) begin() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.starts == nil {
		r.starts = make(map[uint64]time.Time)
	}
	r.nextID++
	r.starts[r.nextID] = time.Now()
	return r.nextID
}

// end removes an RPC. Ending an RPC more than once is harmless.
func (r *inFlightRPCs) end(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.starts, id)
}

// oldestAge returns how long the oldest open RPC has been open at now, or 0 when none is.
func (r *inFlightRPCs) oldestAge(now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	var oldest time.Duration
	for _, start := range r.starts {
		oldest = max(oldest, now.Sub(start))
	}
	return oldest
}

// trackingClient records the RPCs of an ABFSClient in inFlightRPCs. An object read stays
// open until its reader is closed, as the RPC does.
type trackingClient struct {
	ABFSClient
	rpcs *inFlightRPCs
}

var _ ABFSClient = (*trackingClient)(nil)

func (c *trackingClient) AddMapResult(ctx context.Context, from, to entities.Hash) error {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.AddMapResult(ctx, from, to)
}

func (c *trackingClient) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	id := c.rpcs.begin()
	reader, err := c.ABFSClient.GetObject(ctx, key)
	if err != nil || reader == nil {
		c.rpcs.end(id)
		return reader, err
	}
	// The wrapper is a plain ContentReader, so a bundled reader loses that type here; the
	// gRPC client returns plain ContentReaders.
	return remote.NewContentReaderWithCustomClose(reader, func() error {
		defer c.rpcs.end(id)
		return reader.Close()
	}), nil
}

func (c *trackingClient) GetRefs(ctx context.Context, server string, project string, refs []string) ([]entities.Hash, error) {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.GetRefs(ctx, server, project, refs)
}

func (c *trackingClient) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.Headers(ctx, keys)
}

func (c *trackingClient) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.SHA256(ctx, keys)
}

func (c *trackingClient) MapResults(ctx context.Context, id entities.Hash) ([]entities.Hash, error) {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.MapResults(ctx, id)
}

func (c *trackingClient) PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.PutObject(ctx, key, reader)
}

func (c *trackingClient) PutRefs(ctx context.Context, server string, project string, refs []entities.RefHash) error {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.PutRefs(ctx, server, project, refs)
}

func (c *trackingClient) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]entities.ProjectRefHash, error) {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.SearchRefs(ctx, server, project, refPrefix, limit)
}

func (c *trackingClient) Mark(ctx context.Context, hash entities.Hash, key, value string) (int64, error) {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.Mark(ctx, hash, key, value)
}

func (c *trackingClient) Version(ctx context.Context) (string, error) {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.Version(ctx)
}

func (c *trackingClient) WhoAmI(ctx context.Context) (string, error) {
	defer c.rpcs.end(c.rpcs.begin())
	return c.ABFSClient.WhoAmI(ctx)
}

// trackServerRPCs wraps cli in a trackingClient when a metrics provider is configured and
// recording, so that without metrics the client is used as it is. The config decides, so a
// provider name that is not registered still wraps it.
func (c *CacheMan) trackServerRPCs(config *instance.Config, cli ABFSClient) ABFSClient {
	if config.MetricsProvider == "" || !config.RecordClientMetrics {
		return cli
	}
	return &trackingClient{ABFSClient: cli, rpcs: &c.serverRPCs}
}

// unwrapABFSClient returns the client that a trackingClient wraps, or client itself.
func unwrapABFSClient(client ABFSClient) ABFSClient {
	if t, ok := client.(*trackingClient); ok {
		return t.ABFSClient
	}
	return client
}
