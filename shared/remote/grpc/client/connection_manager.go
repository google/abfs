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
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"github.com/google/abfs/third_party/rendezvous"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
	abfsgrpc "github.com/google/abfs/shared/remote/grpc"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
	"github.com/google/abfs/shared/remote/servers/auth"
)

const tlsHelpText = `
The server may have TLS handshaking enabled. Please do the following:
1. Set auth-type to either 'gcpuser' or 'gcpsa': 'abfs --auth-type gcpuser config -w'
2. If the server uses a private CA, place its root CA certificate (PEM) at <instance dir>/abfs_root_ca.crt
`

// retryPolicy allows gRPC to transparently retry on transient errors
const retryPolicy = `{
  "methodConfig": [{
    "name": [{}],
    "retryPolicy": {
      "maxAttempts": 5,
      "initialBackoff": "0.5s",
      "maxBackoff": "5s",
      "backoffMultiplier": 2,
      "retryableStatusCodes": ["UNAVAILABLE", "UNKNOWN"]
    }
  }]
}`

// connectionManager handles compression via remote.CompressingProvider
type connectionManager struct {
	clients map[string]remote.RemoteProvider
	conns   map[string]*grpc.ClientConn

	hasher *rendezvous.Hasher

	bytesSent     atomic.Int64
	lastSent      atomic.Int64
	bytesReceived atomic.Int64
	lastReceived  atomic.Int64

	tokenSource     auth.TokenSource
	rootCAFile      string
	disableTLS      bool
	skipHealthCheck bool
}

var _ abfsgrpc.BytesStatHandler = (*connectionManager)(nil)

func (cm *connectionManager) AddBytesSent(n int64) {
	cm.bytesSent.Add(n)
	cm.lastSent.Store(time.Now().UnixNano())
}

func (cm *connectionManager) AddBytesReceived(n int64) {
	cm.bytesReceived.Add(n)
	cm.lastReceived.Store(time.Now().UnixNano())
}

func newConnectionManager(servers []string, tokenSource auth.TokenSource, rootCAFile string, disableTLS bool, skipHealthCheck bool) (*connectionManager, error) {
	cm := &connectionManager{
		clients:         make(map[string]remote.RemoteProvider, len(servers)),
		conns:           make(map[string]*grpc.ClientConn, len(servers)),
		hasher:          rendezvous.New(),
		tokenSource:     tokenSource,
		rootCAFile:      rootCAFile,
		disableTLS:      disableTLS,
		skipHealthCheck: skipHealthCheck,
	}

	if err := cm.dial(servers); err != nil {
		if err := cm.close(); err != nil {
			log.Printf("Failed to close connection manager: %v", err)
		}
		return nil, err
	}

	return cm, nil
}

func (cm *connectionManager) dial(servers []string) error {
	var g errgroup.Group
	var mu sync.Mutex
	for index, server := range servers {
		// dialing in parallel because the connections wait for healthy before returning
		g.Go(func() error {
			conn, err := abfsgrpc.GrpcDialHealthy(&abfsgrpc.GrpcDialOpts{
				ServiceName:      pb.ABFS_ServiceDesc.ServiceName,
				Address:          server,
				BytesStatHandler: cm,
				ClientVersion:    clientVersion,
				ServerMinVersion: serverMinVersion,
				TokenSource:      cm.tokenSource,
				RootCAFile:       cm.rootCAFile,
				RetryPolicy:      retryPolicy,
				DisableTLS:       cm.disableTLS,
				SkipHealthCheck:  cm.skipHealthCheck,
			})
			if err != nil {
				helpText := ""
				if strings.Contains(err.Error(), "error reading server preface: EOF") {
					helpText = tlsHelpText
				}
				return fmt.Errorf("error making a direct connection to address %q: %w%s", server, err, helpText)
			}

			// TODO: need to use a consistent value here like full VM name instead of this index-based value
			s := fmt.Sprintf("worker-%d", index)
			provider := remote.NewCompressingProvider(abfsgrpc.NewPBClient(pb.NewABFSClient(conn)))
			mu.Lock()
			cm.clients[s] = provider
			cm.conns[s] = conn
			cm.hasher.Add(s)
			mu.Unlock()
			return nil
		})
	}

	return g.Wait()
}

func (cm *connectionManager) getRefs() remote.RemoteProvider {
	// FIXME all refs data need to go to single server, we need to designate one
	return cm.get(entities.ZeroHash)
}

func (cm *connectionManager) get(h Hash) remote.RemoteProvider {
	// using 4 characters of hash because that's the git minimum according to
	// https://git-scm.com/book/en/v2/Git-Tools-Revision-Selection#_short_sha_1
	// in the future we might add support for abbreviated hash retrieval
	return cm.clients[cm.hasher.Get(hashStr(h))]
}

func (cm *connectionManager) close() error {
	errs := make([]error, len(cm.clients))
	i := 0
	for _, conn := range cm.conns {
		if conn != nil {
			errs[i] = conn.Close()
		}
		i++
	}

	return errors.Join(errs...)
}

func (cm *connectionManager) healthCheck(service string) error {
	var g errgroup.Group
	for _, conn := range cm.conns {
		g.Go(func() error { return abfsgrpc.HealthCheck(conn, service) })
	}
	return g.Wait()
}

func hashStr(h Hash) string {
	return h.String()[:4]
}
