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
	"errors"
	"math/rand"
	"slices"
	"sync"

	"google.golang.org/grpc"
)

type RandomConnectionManager struct {
	mu    sync.RWMutex
	conns []*grpc.ClientConn
}

// NewRandomConnectionManager creates a new connection manager that selects a connection randomly.
func NewRandomConnectionManager(conns []*grpc.ClientConn) *RandomConnectionManager {
	// Make a copy to avoid modifying the caller's slice.
	c := make([]*grpc.ClientConn, len(conns))
	copy(c, conns)
	return &RandomConnectionManager{conns: c}
}

func (m *RandomConnectionManager) Select() (*grpc.ClientConn, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.conns) == 0 {
		return nil, errors.New("no connections available")
	}
	return m.conns[rand.Intn(len(m.conns))], nil
}

// Add adds a new connection to the manager.
func (m *RandomConnectionManager) Add(conn *grpc.ClientConn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns = append(m.conns, conn)
}

// Remove removes a connection from the manager.
func (m *RandomConnectionManager) Remove(conn *grpc.ClientConn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns = slices.DeleteFunc(m.conns, func(c *grpc.ClientConn) bool {
		return c == conn
	})
}
