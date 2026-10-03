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

package servers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"

	"google.golang.org/grpc/peer"
)

type LoggingServer struct {
	server            remote.RemoteCloser
	log               *log.Logger
	maxItemsToList    int
	samplingRate      float64
	durationThreshold time.Duration
}

var _ remote.RemoteCloser = (*LoggingServer)(nil)

func NewLoggingServer(server remote.RemoteCloser, log *log.Logger, maxItemsToList int, samplingRate float64) *LoggingServer {
	return &LoggingServer{
		server:            server,
		log:               log,
		maxItemsToList:    maxItemsToList,
		samplingRate:      samplingRate,
		durationThreshold: 10 * time.Second,
	}
}

func (s *LoggingServer) shouldLog(start time.Time, err error) bool {
	if err != nil {
		return true
	}
	if time.Since(start) > s.durationThreshold {
		return true
	}

	if s.samplingRate >= 1.0 {
		return true
	}
	if s.samplingRate <= 0 {
		return false
	}
	return rand.Float64() < s.samplingRate
}

func (s *LoggingServer) logFunc(ctx context.Context, start time.Time, format string, err error, args ...any) {
	if !s.shouldLog(start, err) {
		return
	}
	if err != nil {
		format += " err=%v"
		args = append(args, err)
	}
	var clientIP string
	if p, ok := peer.FromContext(ctx); ok {
		clientIP = p.Addr.String()
	}
	user, ok := remote.UserFromContext(ctx)
	if !ok {
		user = remote.User{
			Email: "unknown",
		}
	}

	userName := strings.SplitN(user.Email, "@", 2)[0]
	newArgs := []any{clientIP, userName, time.Since(start).Seconds()}

	s.log.Printf("%s (%s) %.3f "+format, append(newArgs, args...)...)
}

func (s *LoggingServer) hashesToLog(hashes []remote.Hash) string {
	if len(hashes) > s.maxItemsToList {
		return fmt.Sprintf("[%d]", len(hashes))
	}
	return fmt.Sprintf("%v", hashes)
}

// Headers implements remote.RemoteProvider.
func (s *LoggingServer) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	start := time.Now()

	headers, err := s.server.Headers(ctx, keys)

	notFounds := 0
	for _, header := range headers {
		if header.IsEmpty() {
			notFounds++
		}
	}
	s.logFunc(ctx, start, "Headers(%s): not-found=%d", err, s.hashesToLog(keys), notFounds)
	return headers, err
}

// SHA256s implements remote.RemoteProvider.
func (s *LoggingServer) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	start := time.Now()

	shas, err := s.server.SHA256(ctx, keys)

	notFounds := 0
	for _, sha := range shas {
		if sha.IsZero() {
			notFounds++
		}
	}
	s.logFunc(ctx, start, "SHA256(%s): not-found=%d", err, s.hashesToLog(keys), notFounds)
	return shas, err
}

// GetObject implements remote.RemoteProvider.
func (s *LoggingServer) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	start := time.Now()

	origReader, err := s.server.GetObject(ctx, key)

	s.logFunc(ctx, start, "GetObject(%s)", err, key)

	var reader remote.ContentReadCloser
	if origReader != nil {
		reader = remote.NewContentReaderWithCustomClose(origReader, func() error {
			err := origReader.Close()
			s.logFunc(ctx, start, "ReadObject(%s)", err, key)
			return err
		})
	}
	return reader, err
}

// PutObject implements remote.RemoteProvider.
func (s *LoggingServer) PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
	start := time.Now()

	err := s.server.PutObject(ctx, key, reader)

	s.logFunc(ctx, start, "PutObject(%s)", err, key)

	return err
}

// MapResults implements remote.RemoteProvider.
func (s *LoggingServer) MapResults(ctx context.Context, from remote.Hash) ([]remote.Hash, error) {
	start := time.Now()

	result, err := s.server.MapResults(ctx, from)

	s.logFunc(ctx, start, "MapResults(%s): result=%s", err, from, s.hashesToLog(result))

	return result, err
}

// AddMapResult implements remote.RemoteProvider.
func (s *LoggingServer) AddMapResult(ctx context.Context, from remote.Hash, to remote.Hash) error {
	start := time.Now()

	err := s.server.AddMapResult(ctx, from, to)

	s.logFunc(ctx, start, "AddMapResult(%s -> %s)", err, from, to)

	return err
}

// GetRefs implements remote.RemoteProvider.
func (s *LoggingServer) GetRefs(ctx context.Context, server string, project string, refs []string) ([]remote.Hash, error) {
	start := time.Now()

	result, err := s.server.GetRefs(ctx, server, project, refs)

	s.logFunc(ctx, start, "GetRefs(%s,%s,[%s]): result=%s", err, server, project, strings.Join(refs, ","), s.hashesToLog(result))

	return result, err
}

// SearchRefs implements remote.RemoteProvider.
func (s *LoggingServer) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]remote.ProjectRefHash, error) {
	start := time.Now()

	result, err := s.server.SearchRefs(ctx, server, project, refPrefix, limit)

	s.logFunc(ctx, start, "SearchRefs(%s,%s,%q,%d): result=%d", err, server, project, refPrefix, limit, len(result))

	return result, err
}

// PutRefs implements remote.RemoteProvider.
func (s *LoggingServer) PutRefs(ctx context.Context, server string, project string, refs []remote.RefHash) error {
	start := time.Now()

	err := s.server.PutRefs(ctx, server, project, refs)

	s.logFunc(ctx, start, "PutRefs(%s,%s,[%d])", err, server, project, len(refs))

	return err
}

// Mark implements remote.RemoteProvider.
func (s *LoggingServer) Mark(ctx context.Context, hash remote.Hash, key string, value string) (int64, error) {
	start := time.Now()

	count, err := s.server.Mark(ctx, hash, key, value)

	s.logFunc(ctx, start, "Mark(%s, %s, %s): result=%d", err, hash, key, value, count)

	return count, err
}

// Version implements remote.RemoteProvider.
func (s *LoggingServer) Version(ctx context.Context) (string, error) {
	start := time.Now()

	version, err := s.server.Version(ctx)

	s.logFunc(ctx, start, "Version(): result=%s", err, version)

	return version, err
}

// WhoAmI implements remote.RemoteProvider.
func (s *LoggingServer) WhoAmI(ctx context.Context) (string, error) {
	return "", errors.New("unimplemented")
}

// Close implements remote.RemoteCloser.
func (s *LoggingServer) Close() error {
	start := time.Now()

	err := s.server.Close()

	s.logFunc(context.Background(), start, "Close()", err)

	return err
}
