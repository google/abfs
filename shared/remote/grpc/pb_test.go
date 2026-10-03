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
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
)

const bufSize = 10 * 1024 * 1024

func grpcPipe(t *testing.T, server remote.RemoteProvider, opts ...grpc.ServerOption) (remote.RemoteProvider, context.CancelFunc) {
	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer(opts...)
	pb.RegisterABFSServer(grpcServer, NewPBServer(server))

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("Server exited with error: %v", err)
		}
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(dialer))

	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}

	return &PBClient{pb: pb.NewABFSClient(conn)}, func() {
		if err := conn.Close(); err != nil {
			t.Logf("Failed to close the client: %v\n", err)
		}
		grpcServer.Stop()
		if err := lis.Close(); err != nil {
			t.Logf("Failed to close the server: %v\n", err)
		}
	}
}

type ServerImpl struct {
	HeadersImpl      func(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error)
	SHA256Impl       func(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error)
	GetObjectImpl    func(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error)
	PutObjectImpl    func(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error
	MapResultsImpl   func(ctx context.Context, from remote.Hash) ([]remote.Hash, error)
	AddMapResultImpl func(ctx context.Context, from remote.Hash, to remote.Hash) error
	GetRefsImpl      func(ctx context.Context, server string, project string, refs []string) ([]remote.Hash, error)
	SearchRefsImpl   func(ctx context.Context, server string, project string, refPrefix string, limit int) ([]remote.ProjectRefHash, error)
	PutRefsImpl      func(ctx context.Context, server string, project string, refs []remote.RefHash) error
	MarkImpl         func(ctx context.Context, hash remote.Hash, key string, value string) (int64, error)
	VersionImpl      func(ctx context.Context) (string, error)
	WhoAmIImpl       func(ctx context.Context) (string, error)
}

// AddMapResult implements remote.RemoteProvider.
func (s *ServerImpl) AddMapResult(ctx context.Context, from remote.Hash, to remote.Hash) error {
	return s.AddMapResultImpl(ctx, from, to)
}

// GetObject implements remote.RemoteProvider.
func (s *ServerImpl) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	return s.GetObjectImpl(ctx, key)
}

// GetRefs implements remote.RemoteProvider.
func (s *ServerImpl) GetRefs(ctx context.Context, server string, project string, refs []string) ([]remote.Hash, error) {
	return s.GetRefsImpl(ctx, server, project, refs)
}

// Headers implements remote.RemoteProvider.
func (s *ServerImpl) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	return s.HeadersImpl(ctx, keys)
}

// MapResults implements remote.RemoteProvider.
func (s *ServerImpl) MapResults(ctx context.Context, from remote.Hash) ([]remote.Hash, error) {
	return s.MapResultsImpl(ctx, from)
}

// Mark implements remote.RemoteProvider.
func (s *ServerImpl) Mark(ctx context.Context, hash remote.Hash, key string, value string) (int64, error) {
	return s.MarkImpl(ctx, hash, key, value)
}

// PutObject implements remote.RemoteProvider.
func (s *ServerImpl) PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
	return s.PutObjectImpl(ctx, key, reader)
}

// PutRefs implements remote.RemoteProvider.
func (s *ServerImpl) PutRefs(ctx context.Context, server string, project string, refs []remote.RefHash) error {
	return s.PutRefsImpl(ctx, server, project, refs)
}

// SearchRefs implements remote.RemoteProvider.
func (s *ServerImpl) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]remote.ProjectRefHash, error) {
	return s.SearchRefsImpl(ctx, server, project, refPrefix, limit)
}

// SHA256 implements remote.RemoteProvider.
func (s *ServerImpl) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	return s.SHA256Impl(ctx, keys)
}

// Version implements remote.RemoteProvider.
func (s *ServerImpl) Version(ctx context.Context) (string, error) {
	return s.VersionImpl(ctx)
}

// WhoAmI implements remote.RemoteProvider.
func (s *ServerImpl) WhoAmI(ctx context.Context) (string, error) {
	return s.WhoAmIImpl(ctx)
}

func TestVersion(t *testing.T) {
	const myVer = "1.2.3"

	server := &ServerImpl{
		VersionImpl: func(ctx context.Context) (string, error) {
			return myVer, nil
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	version, err := client.Version(context.Background())
	require.NoError(t, err)
	require.Equal(t, myVer, version)
}

func TestGetObjectSimple(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	var myContent = "hello world!\n"

	server := &ServerImpl{
		GetObjectImpl: func(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
			assert.Equal(t, myKey, key)

			return remote.NewBlobReaderFromBytes([]byte(myContent)), nil
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	reader, err := client.GetObject(context.Background(), myKey)
	require.NoError(t, err)
	content, err := objects.ReadBlob(reader)
	require.NoError(t, err)
	require.Equal(t, myContent, string(content))
}

func TestGetObjectHuge(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	var myContent = bytes.Repeat([]byte("Hello really huge line which is exactly 50 bytes!\n"), 1024*1024) // 50 MB

	server := &ServerImpl{
		GetObjectImpl: func(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
			assert.Equal(t, myKey, key)

			return remote.NewBlobReaderFromBytes([]byte(myContent)), nil
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	reader, err := client.GetObject(context.Background(), myKey)
	require.NoError(t, err)
	content, err := objects.ReadBlob(reader)
	require.NoError(t, err)
	require.Equal(t, myContent, content)
}

func TestGetObjectNotFound(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")

	server := &ServerImpl{
		GetObjectImpl: func(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
			assert.Equal(t, myKey, key)

			return nil, entities.ErrNotExist
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	_, err := client.GetObject(context.Background(), myKey)
	require.True(t, entities.IsNotExist(err))
}

func TestGetObjectRandomError(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")

	server := &ServerImpl{
		GetObjectImpl: func(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
			assert.Equal(t, myKey, key)

			return nil, errors.New("random error")
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	_, err := client.GetObject(context.Background(), myKey)
	require.ErrorContains(t, err, "random error")
}

func TestGetObjectReaderError(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")

	server := &ServerImpl{
		GetObjectImpl: func(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
			assert.Equal(t, myKey, key)

			reader := iotest.ErrReader(errors.New("reader error"))
			return remote.NewContentReader(io.NopCloser(reader), remote.EncodingRaw), nil
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	_, err := client.GetObject(context.Background(), myKey)
	require.ErrorContains(t, err, "reader error")
}

func TestGetObjectReaderError2(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	var myContent = "hello world!\n"

	server := &ServerImpl{
		GetObjectImpl: func(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
			assert.Equal(t, myKey, key)

			reader := objects.PrependHeaderReader(strings.NewReader(myContent), "blob", int64(len(myContent)))

			reader = io.MultiReader(reader, iotest.ErrReader(errors.New("reader error")))
			return remote.NewContentReader(io.NopCloser(reader), remote.EncodingRaw), nil
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	err := func() error {
		reader, err := client.GetObject(context.Background(), myKey)
		if err != nil {
			return err
		}
		_, err = objects.ReadBlob(reader)
		if err != nil {
			return err
		}
		return err
	}()

	require.ErrorContains(t, err, "reader error")
}

func TestPutObjectSimple(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	var myContent = "hello world!\n"

	server := &ServerImpl{
		PutObjectImpl: func(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
			assert.Equal(t, myKey, key)

			content, err := objects.ReadBlob(reader)
			assert.NoError(t, err)

			assert.Equal(t, string(content), myContent)

			return reader.Close()
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	reader := remote.NewBlobReaderFromBytes([]byte(myContent))
	err := client.PutObject(context.Background(), myKey, reader)
	require.NoError(t, err)
}

func TestPutObjectHuge(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	var myContent = bytes.Repeat([]byte("Hello really huge line which is exactly 50 bytes!\n"), 1024*1024) // 50 MB

	server := &ServerImpl{
		PutObjectImpl: func(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
			assert.Equal(t, myKey, key)

			content, err := objects.ReadBlob(reader)
			assert.NoError(t, err)

			assert.Equal(t, content, myContent)

			return reader.Close()
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	reader := remote.NewBlobReaderFromBytes([]byte(myContent))
	err := client.PutObject(context.Background(), myKey, reader)
	require.NoError(t, err)
}

func TestPutObjectLateError(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	var myContent = "hello world!\n"

	server := &ServerImpl{
		PutObjectImpl: func(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
			assert.Equal(t, myKey, key)

			content, err := objects.ReadBlob(reader)
			assert.NoError(t, err)

			assert.Equal(t, string(content), myContent)

			return errors.New("late error")
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	reader := remote.NewBlobReaderFromBytes([]byte(myContent))
	err := client.PutObject(context.Background(), myKey, reader)
	require.ErrorContains(t, err, "late error")
}

func TestPutObjectEarlyError(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	var myContent = "hello world!\n"

	server := &ServerImpl{
		PutObjectImpl: func(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
			assert.Equal(t, myKey, key)
			return errors.New("early error")
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	reader := remote.NewBlobReaderFromBytes([]byte(myContent))
	err := client.PutObject(context.Background(), myKey, reader)
	require.ErrorContains(t, err, "early error")
}

func TestPutObjectStreamError(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	var myContent = "hello world!\n"

	server := &ServerImpl{
		PutObjectImpl: func(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
			assert.Equal(t, myKey, key)

			content, err := objects.ReadBlob(reader)
			assert.NoError(t, err)

			assert.Equal(t, string(content), myContent)

			return reader.Close()
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	reader := objects.PrependHeaderReader(strings.NewReader(myContent), "blob", int64(len(myContent)))
	reader = io.MultiReader(reader, iotest.ErrReader(errors.New("reader error")))
	err := client.PutObject(context.Background(), myKey, remote.NewContentReader(io.NopCloser(reader), remote.EncodingRaw))
	require.ErrorContains(t, err, "reader error")
}

func TestPutObjectInterceptorError(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	var myContent = "hello world!\n"

	server := &ServerImpl{
		PutObjectImpl: func(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
			return nil
		},
	}

	interceptor := func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return status.Error(codes.PermissionDenied, "permission denied")
	}

	client, stop := grpcPipe(t, server, grpc.StreamInterceptor(interceptor))
	defer stop()

	reader := remote.NewBlobReaderFromBytes([]byte(myContent))
	err := client.PutObject(context.Background(), myKey, reader)
	require.Error(t, err)
	require.ErrorContains(t, err, "permission denied")
}

func TestHeadersSimple(t *testing.T) {
	var myKeys = []remote.Hash{
		entities.NewHash("a0423896973644771497bdc03eb99d5281615b50"),
		entities.NewHash("a0423896973644771497bdc03eb99d5281615b51"),
		entities.NewHash("a0423896973644771497bdc03eb99d5281615b52"),
	}

	headers := []entities.ObjectHeader{
		{
			Type: "blob",
			Size: 123456,
		},
		{
			Type: "",
			Size: 0,
		},
		{
			Type: "tree",
			Size: 123,
		},
	}

	server := &ServerImpl{
		HeadersImpl: func(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
			assert.Equal(t, myKeys, keys)
			return headers, nil
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	got, err := client.Headers(context.Background(), myKeys)
	require.NoError(t, err)
	require.Equal(t, headers, got)
	require.False(t, headers[0].IsEmpty())
	require.True(t, headers[1].IsEmpty())
}

func TestHeadersLong(t *testing.T) {
	n := 100000 // long enough to make sure of input split

	myKeys := make([]remote.Hash, n)
	headers := make([]entities.ObjectHeader, n)
	hdrMap := make(map[Hash]entities.ObjectHeader, n)

	for i := range n {
		myKeys[i] = entities.NewHash(fmt.Sprintf("a0423896973644771497bdc03eb99d%010x", i))
		headers[i] = entities.ObjectHeader{
			Type: "blob",
			Size: 123456 + int64(i),
		}
		hdrMap[myKeys[i]] = headers[i]
	}

	server := &ServerImpl{
		HeadersImpl: func(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
			headers := make([]entities.ObjectHeader, len(keys))
			for i, key := range keys {
				hdr, ok := hdrMap[key]
				if !ok {
					return nil, fmt.Errorf("key %s not found in map", key)
				}
				headers[i] = hdr
			}
			return headers, nil
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	got, err := client.Headers(context.Background(), myKeys)
	require.NoError(t, err)
	require.Equal(t, headers, got)
}

func TestAddMapResultPermissionDenied(t *testing.T) {
	var myKey = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")
	var myValue = entities.NewHash("a0423896973644771497bdc03eb99d5281615b52")

	server := &ServerImpl{
		AddMapResultImpl: func(ctx context.Context, from remote.Hash, to remote.Hash) error {
			return &remote.ErrPermissionDenied{Action: "writing map result"}
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	err := client.AddMapResult(context.Background(), myKey, myValue)
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.PermissionDenied, st.Code())
	require.Contains(t, st.Message(), "writing map result")
}

func TestPutRefsPermissionDenied(t *testing.T) {
	server := &ServerImpl{
		PutRefsImpl: func(ctx context.Context, server string, project string, refs []remote.RefHash) error {
			return &remote.ErrPermissionDenied{Action: `"refs/heads/main"`}
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	err := client.PutRefs(context.Background(), "myserver", "myproject", []remote.RefHash{{Ref: "refs/heads/main", Hash: entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")}})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.PermissionDenied, st.Code())
	require.Contains(t, st.Message(), `"refs/heads/main"`)
}

func TestSHA256Simple(t *testing.T) {
	var myKeys = []remote.Hash{
		entities.NewHash("a0423896973644771497bdc03eb99d5281615b50"),
		entities.NewHash("a0423896973644771497bdc03eb99d5281615b51"),
		entities.NewHash("a0423896973644771497bdc03eb99d5281615b52"),
	}

	shas := []entities.SHA256{
		entities.SHA256(sha256.Sum256([]byte("hello"))),
		entities.SHA256(sha256.Sum256([]byte("world"))),
		entities.SHA256(sha256.Sum256([]byte("test"))),
	}

	server := &ServerImpl{
		SHA256Impl: func(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
			assert.Equal(t, myKeys, keys)
			return shas, nil
		},
	}

	client, stop := grpcPipe(t, server)
	defer stop()

	got, err := client.SHA256(context.Background(), myKeys)
	require.NoError(t, err)
	require.Equal(t, shas, got)
}
