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
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// freezingProxy forwards TCP connections to a server. While frozen it holds every byte and
// does not forward new connections, so the client sees a server whose kernel still accepts
// and acknowledges data but whose process answers nothing, like a stopped server process.
type freezingProxy struct {
	lis    net.Listener
	target string

	mu     sync.Mutex
	cond   *sync.Cond
	frozen bool
	conns  []net.Conn
}

func newFreezingProxy(t *testing.T, target string) *freezingProxy {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &freezingProxy{lis: lis, target: target}
	p.cond = sync.NewCond(&p.mu)
	go p.serve()
	return p
}

func (p *freezingProxy) setFrozen(frozen bool) {
	p.mu.Lock()
	p.frozen = frozen
	p.mu.Unlock()
	p.cond.Broadcast()
}

func (p *freezingProxy) waitUnfrozen() {
	p.mu.Lock()
	for p.frozen {
		p.cond.Wait()
	}
	p.mu.Unlock()
}

func (p *freezingProxy) track(c net.Conn) {
	p.mu.Lock()
	p.conns = append(p.conns, c)
	p.mu.Unlock()
}

func (p *freezingProxy) serve() {
	for {
		c, err := p.lis.Accept()
		if err != nil {
			return
		}
		p.track(c)
		go func() {
			p.waitUnfrozen()
			up, err := net.Dial("tcp", p.target)
			if err != nil {
				_ = c.Close()
				return
			}
			p.track(up)
			go p.pipe(up, c)
			p.pipe(c, up)
		}()
	}
}

func (p *freezingProxy) pipe(dst, src net.Conn) {
	defer func() {
		_ = dst.Close()
		_ = src.Close()
	}()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			p.waitUnfrozen()
			if _, err := dst.Write(buf[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *freezingProxy) Close() {
	p.setFrozen(false)
	_ = p.lis.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
}

func TestGrpcDialHealthy_KeepaliveHungServer(t *testing.T) {
	// grpc-go raises Time to at least 10 s; Timeout can be shorter.
	oldKeepalive := clientKeepalive
	defer func() { clientKeepalive = oldKeepalive }()
	clientKeepalive = keepalive.ClientParameters{Time: 10 * time.Second, Timeout: time.Second}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := grpc.NewServer(ServerKeepalivePolicy())
	hs := health.NewServer()
	serviceName := "test-service"
	hs.SetServingStatus(serviceName, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(s, hs)
	go func() {
		_ = s.Serve(lis)
	}()
	defer s.Stop()

	proxy := newFreezingProxy(t, lis.Addr().String())
	defer proxy.Close()

	conn, err := GrpcDialHealthy(&GrpcDialOpts{
		ServiceName:      serviceName,
		Address:          proxy.lis.Addr().String(),
		BytesStatHandler: mockBytesStatHandler{},
		ClientVersion:    "0.1.2",
		ServerMinVersion: "0.1.1",
		DisableTLS:       true,
	})
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	client := grpc_health_v1.NewHealthClient(conn)
	req := &grpc_health_v1.HealthCheckRequest{Service: serviceName}

	proxy.setFrozen(true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	_, err = client.Check(ctx, req)
	elapsed := time.Since(start)
	require.Equal(t, codes.Unavailable, status.Code(err), "call to a hung server: %v", err)
	require.Less(t, elapsed, clientKeepalive.Time+clientKeepalive.Timeout+5*time.Second)

	proxy.setFrozen(false)
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = client.Check(ctx, req, grpc.WaitForReady(true))
	require.NoError(t, err)
}
