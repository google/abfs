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
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/google/abfs/ares/grpc/proto"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	abfsgrpc "github.com/google/abfs/shared/remote/grpc"
	"github.com/google/abfs/shared/remote/servers/auth"
)

// TODO: add contextual timeout values depending on rule type, etc.
const executeTimeout = time.Duration(2 * time.Hour)

var (
	clientVersion    = "0.1"
	serverMinVersion = "0.1"
)

// retryPolicy allows gRPC to transparently retry on transient errors
const retryPolicy = `{
  "methodConfig": [{
    "name": [{}],
    "retryPolicy": {
      "maxAttempts": 5,
      "initialBackoff": "0.1s",
      "maxBackoff": "1s",
      "backoffMultiplier": 2,
      "retryableStatusCodes": ["UNAVAILABLE", "UNKNOWN"]
    }
  }]
}`

// ARESClient serves the purpose of converting from gRPC conventions to golang conventions
type ARESClient struct {
	// conn stored for health check and closing purposes
	conn *grpc.ClientConn
	pb   pb.ARESClient

	bytesSent     atomic.Int64
	bytesReceived atomic.Int64
}

var _ abfsgrpc.BytesStatHandler = (*ARESClient)(nil)
var _ remote.ARESService = (*ARESClient)(nil)

type ARESClientOpts struct {
	Address         string
	AuthInfo        instance.AuthInfo
	SkipHealthCheck bool
}

func NewARESClient(address string, authInfo instance.AuthInfo) (*ARESClient, error) {
	return NewARESClientWithOptions(&ARESClientOpts{
		Address:         address,
		AuthInfo:        authInfo,
		SkipHealthCheck: false,
	})
}

func NewARESClientWithOptions(o *ARESClientOpts) (*ARESClient, error) {
	c := &ARESClient{}

	tokenSource, err := auth.GenerateTokenSource(o.AuthInfo)
	if err != nil {
		return nil, err
	}

	conn, err := abfsgrpc.GrpcDialHealthy(&abfsgrpc.GrpcDialOpts{
		ServiceName:      pb.ARES_ServiceDesc.ServiceName,
		Address:          o.Address,
		BytesStatHandler: c,
		ClientVersion:    clientVersion,
		ServerMinVersion: serverMinVersion,
		TokenSource:      tokenSource,
		RootCAFile:       o.AuthInfo.RootCAFile,
		RetryPolicy:      retryPolicy,
		DisableTLS:       o.AuthInfo.DisableTLS,
		SkipHealthCheck:  o.SkipHealthCheck,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create ares client for address %q: %v", o.Address, err)
	}
	c.conn = conn
	c.pb = pb.NewARESClient(conn)

	return c, nil
}

// NewAresConnection should only be used when a raw grpc *connection* is needed instead of a full
// client. If a client is needed, use NewARESClient.
func NewARESConnection(address string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	// we aren't using GrpcDialHealthy since we don't need byte counting or version checking across this connection.
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial address %s: %v", address, err)
	}

	if err := abfsgrpc.HealthCheck(conn, ""); err != nil {
		return nil, fmt.Errorf("error during health check: %w", err)
	}
	return conn, nil
}

func (c *ARESClient) Close() error {
	return c.conn.Close()
}

func (c *ARESClient) AddBytesSent(n int64) {
	c.bytesSent.Add(n)
}

func (c *ARESClient) AddBytesReceived(n int64) {
	c.bytesReceived.Add(n)
}

// Execute has the ability to return status updates while execution is in progress,
// but this isn't currently implemented. This can be added if it is useful somewhere.
func (c *ARESClient) Execute(ctx context.Context, evalRequestHash entities.Hash, patchHashes []entities.Hash, storeMapResult, prefetchInputs bool) (entities.Hash, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, executeTimeout, fmt.Errorf("timeout running remote execute for eval request %q", evalRequestHash))
	defer cancel()
	resp, err := c.pb.Execute(ctx, &pb.ExecuteRequest{
		EvalRequest:    abfsgrpc.HashToBytes(evalRequestHash),
		Patches:        abfsgrpc.HashesToHashBytes(patchHashes),
		StoreMapResult: storeMapResult,
		PrefetchInputs: prefetchInputs,
	})
	if err != nil {
		return entities.ZeroHash, fmt.Errorf("execute's gRPC request failed: %w", err)
	}
	return abfsgrpc.HashFromInputBytes(resp.EvalResponse), nil
}

func (c *ARESClient) HealthCheck(ctx context.Context, service string) error {
	return abfsgrpc.HealthCheck(c.conn, service)
}
