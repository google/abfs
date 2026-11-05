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

package gcp

import (
	"context"
	"fmt"
	"regexp"

	"github.com/gorilla/mux"
	"google.golang.org/grpc/peer"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
)

type FilterServer struct {
	server       remote.RemoteCloser
	clientFilter *regexp.Regexp
}

var _ remote.RemoteCloser = (*FilterServer)(nil)

func NewFilterServer(server remote.RemoteCloser, clientFilter *regexp.Regexp) *FilterServer {
	return &FilterServer{
		server:       server,
		clientFilter: clientFilter,
	}
}

func (s *FilterServer) validate(ctx context.Context) error {
	var clientAddr string
	if p, ok := peer.FromContext(ctx); ok {
		clientAddr = p.Addr.String()
	}

	if s.clientFilter != nil && s.clientFilter.MatchString(clientAddr) {
		return fmt.Errorf("cannot serve %s", clientAddr)
	}

	return nil
}

// Headers implements remote.RemoteProvider.
func (s *FilterServer) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}

	return s.server.Headers(ctx, keys)
}

// GetObject implements remote.RemoteProvider.
func (s *FilterServer) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}

	return s.server.GetObject(ctx, key)
}

// PutObject implements remote.RemoteProvider.
func (s *FilterServer) PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
	if err := s.validate(ctx); err != nil {
		return err
	}

	return s.server.PutObject(ctx, key, reader)
}

// MapResults implements remote.RemoteProvider.
func (s *FilterServer) MapResults(ctx context.Context, from remote.Hash) ([]remote.Hash, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}

	return s.server.MapResults(ctx, from)
}

// AddMapResult implements remote.RemoteProvider.
func (s *FilterServer) AddMapResult(ctx context.Context, from remote.Hash, to remote.Hash) error {
	if err := s.validate(ctx); err != nil {
		return err
	}

	return s.server.AddMapResult(ctx, from, to)
}

// GetRefs implements remote.RemoteProvider.
func (s *FilterServer) GetRefs(ctx context.Context, server string, project string, refs []string) ([]remote.Hash, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}

	return s.server.GetRefs(ctx, server, project, refs)
}

// SearchRefs implements remote.RemoteProvider.
func (s *FilterServer) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]remote.ProjectRefHash, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}

	return s.server.SearchRefs(ctx, server, project, refPrefix, limit)
}

// PutRefs implements remote.RemoteProvider.
func (s *FilterServer) PutRefs(ctx context.Context, server string, project string, refs []remote.RefHash) error {
	if err := s.validate(ctx); err != nil {
		return err
	}

	return s.server.PutRefs(ctx, server, project, refs)
}

// XchgRef implements remote.RemoteProvider.
func (s *FilterServer) XchgRef(ctx context.Context, server string, project string, ref string, desired remote.Hash, current remote.Hash) error {
	if err := s.validate(ctx); err != nil {
		return err
	}

	return s.server.XchgRef(ctx, server, project, ref, desired, current)
}

// XchgRefs implements remote.RemoteProvider.
func (s *FilterServer) XchgRefs(ctx context.Context, server string, project string, refs []remote.RefXchg) error {
	if err := s.validate(ctx); err != nil {
		return err
	}

	return s.server.XchgRefs(ctx, server, project, refs)
}

// Mark implements remote.RemoteProvider.
func (s *FilterServer) Mark(ctx context.Context, hash remote.Hash, key string, value string) (int64, error) {
	if err := s.validate(ctx); err != nil {
		return 0, err
	}

	return s.server.Mark(ctx, hash, key, value)
}

// Version implements remote.RemoteProvider.
func (s *FilterServer) Version(ctx context.Context) (string, error) {
	if err := s.validate(ctx); err != nil {
		return "", err
	}

	return s.server.Version(ctx)
}

// WhoAmI implements remote.RemoteProvider.
func (s *FilterServer) WhoAmI(ctx context.Context) (string, error) {
	if err := s.validate(ctx); err != nil {
		return "", err
	}
	return s.server.WhoAmI(ctx)
}

// RegisterHTTPRoutes registers HTTP routes with the router if the underlying server supports it.
func (s *FilterServer) RegisterHTTPRoutes(r *mux.Router) {
	if h, ok := s.server.(interface{ RegisterHTTPRoutes(*mux.Router) }); ok {
		h.RegisterHTTPRoutes(r)
	}
}

// HealthProbes returns the backend health checks of the underlying server, if it has any.
func (s *FilterServer) HealthProbes() map[string]func(context.Context) error {
	if h, ok := s.server.(interface {
		HealthProbes() map[string]func(context.Context) error
	}); ok {
		return h.HealthProbes()
	}
	return nil
}

// Close implements remote.RemoteCloser.
func (s *FilterServer) Close() error {
	if err := s.validate(context.Background()); err != nil {
		return err
	}

	return s.server.Close()
}

// SHA256 implements remote.RemoteProvider.
func (s *FilterServer) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	return s.server.SHA256(ctx, keys)
}
