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

package grpc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"github.com/google/abfs/ares/grpc/client"
	"github.com/google/abfs/remoteconfig"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/remote"
	abfsgrpc "github.com/google/abfs/shared/remote/grpc"
)

var connGracefulTimeout = 30 * time.Minute

type connEntry struct {
	conn *grpc.ClientConn
	wg   *sync.WaitGroup
}

func (c *connEntry) Wait() error {
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(connGracefulTimeout):
		return fmt.Errorf("active requests did not finish after waiting %s", connGracefulTimeout)
	}
}

type RandomConnMgr struct {
	*abfsgrpc.RandomConnectionManager
	conns map[string]*connEntry
	store *remote.RemoteStore
	// stop is closed when Close() is called
	stop chan struct{}
	// bgWG is used to wait for the background goroutine to finish. Otherwise there are race conditions opening
	// and closing connections.
	bgWG sync.WaitGroup

	connFactory func(string, ...grpc.DialOption) (*grpc.ClientConn, error)
}

// NewRandomConnMgr will return a connection manager that can be used for selecting an ARES connection
// for a request. It will start a background goroutine to manage the connections as they become active/inactive.
// Call Stop() to terminate the goroutine.
func NewRandomConnMgr(ctx context.Context, abfsServer remote.RemoteCloser) *RandomConnMgr {
	return newRandomConnMgr(ctx, abfsServer, client.NewARESConnection)
}

func newRandomConnMgr(ctx context.Context, abfsServer remote.RemoteCloser, connFactory func(string, ...grpc.DialOption) (*grpc.ClientConn, error)) *RandomConnMgr {
	conns := make(map[string]*connEntry)

	connMgr := &RandomConnMgr{
		RandomConnectionManager: abfsgrpc.NewRandomConnectionManager(nil),
		conns:                   conns,
		// the abfsServer is the actual backing store of the ABFS CAS, so it is compressed
		store:       remote.NewRemoteStore(remote.NewCompressingCloser(abfsServer)),
		stop:        make(chan struct{}),
		connFactory: connFactory,
	}

	connMgr.bgWG.Add(1)
	go connMgr.manageConnsBackground(ctx)

	return connMgr
}

func (m *RandomConnMgr) manageConnsBackground(ctx context.Context) {
	defer m.bgWG.Done()

	hp, err := hashpath.Parse(ConfigARESServersHashPath)
	if err != nil {
		log.Printf("ERROR: Will not retry. Could not parse ares server config hashpath %q: %v", ConfigARESServersHashPath, err)
		return
	}

	// Create a context that we can cancel when m.stop is closed.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Watch for stop signal to cancel context
	go func() {
		select {
		case <-m.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	remoteconfig.WatchConfig(ctx, hp, m.store, func(aresConfig ARESServerConfig, hash entities.Hash) {
		newServers := make(map[string]struct{})
		for _, server := range aresConfig.Servers {
			if !server.Enabled {
				continue
			}
			addr := server.Address
			newServers[addr] = struct{}{}
			if _, ok := m.conns[addr]; !ok {
				entry, err := m.createConnection(addr)
				if err != nil {
					log.Printf("WARNING: failed to connect to new ARES server %q: %v", addr, err)
					continue
				}
				m.conns[addr] = entry
				m.Add(entry.conn)
				log.Printf("Added ARES server %q to proxy", addr)
			}
		}

		for addr, entry := range m.conns {
			if _, ok := newServers[addr]; !ok {
				m.Remove(entry.conn)
				// Launch a goroutine to wait for active RPCs before closing
				go func() {
					if err := entry.Wait(); err != nil {
						log.Printf("WARNING: error waiting for connection to %q to drain active requests: %v", addr, err)
					}
					if err := entry.conn.Close(); err != nil {
						log.Printf("WARNING: failed to close connection to %q: %v", addr, err)
					}
				}()
				delete(m.conns, addr)
				log.Printf("Removed ARES server %q from proxy", addr)
			}
		}
	})
}

func (m *RandomConnMgr) createConnection(addr string) (*connEntry, error) {
	wg := &sync.WaitGroup{}
	conn, err := m.connFactory(addr,
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			wg.Add(1)
			defer wg.Done()
			return invoker(ctx, method, req, reply, cc, opts...)
		}),
		grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			wg.Add(1)
			s, err := streamer(ctx, desc, cc, method, opts...)
			if err != nil {
				wg.Done()
				return nil, err
			}
			// using a once in case clients call RecvMsg multiple times after the stream is closed
			var once sync.Once
			// done is handled by the wrapper stream
			return &wgStream{ClientStream: s, wgDone: func() { once.Do(func() { wg.Done() }) }}, nil
		}),
	)
	if err != nil {
		return nil, err
	}
	return &connEntry{conn: conn, wg: wg}, nil
}

type wgStream struct {
	grpc.ClientStream
	wgDone func()
}

func (s *wgStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	// We decrement the WaitGroup when the stream has EOF or other error.
	// This assumes the client calls RecvMsg until error, which is standard practice.
	if err != nil {
		s.wgDone()
	}
	return err
}

func (m *RandomConnMgr) Close() error {
	close(m.stop)
	m.bgWG.Wait()

	var g errgroup.Group
	for addr, entry := range m.conns {
		// might need to put a timeout here if it tries to wait for all connections to close. Some ARES requests could take >30 minutes for long build commands.
		g.Go(func() error {
			if err := entry.Wait(); err != nil {
				log.Printf("WARNING: error waiting for connection to %q to drain active requests: %v", addr, err)
			}
			return entry.conn.Close()
		})
	}
	err := g.Wait()
	return errors.Join(err, m.store.Close())
}
