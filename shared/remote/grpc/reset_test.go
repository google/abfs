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
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/google/abfs/abfslog"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/remote"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
)

// resetPipe connects a client to server through a gRPC server that resets the next resets streams with
// RST_STREAM INTERNAL_ERROR, as a load balancer does when it drops a connection. The server sends a
// response header larger than the client accepts, which makes grpc-go reset the stream before the
// first message.
func resetPipe(t *testing.T, server remote.RemoteProvider, resets *atomic.Int32) remote.RemoteProvider {
	return newResetPipe(t, server, resets, false)
}

// trailerResetPipe is resetPipe with the over-size metadata in the trailer, so the stream is reset
// after the handler sent its messages.
func trailerResetPipe(t *testing.T, server remote.RemoteProvider, resets *atomic.Int32) remote.RemoteProvider {
	return newResetPipe(t, server, resets, true)
}

func newResetPipe(t *testing.T, server remote.RemoteProvider, resets *atomic.Int32, inTrailer bool) remote.RemoteProvider {
	t.Helper()
	const maxHeaderListSize = 1024
	resetMD := func() metadata.MD {
		return metadata.Pairs("pad", strings.Repeat("x", 2*maxHeaderListSize))
	}
	consumeReset := func() bool {
		for {
			n := resets.Load()
			if n <= 0 {
				return false
			}
			if resets.CompareAndSwap(n, n-1) {
				return true
			}
		}
	}

	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if consumeReset() {
				set := grpc.SetHeader
				if inTrailer {
					set = grpc.SetTrailer
				}
				if err := set(ctx, resetMD()); err != nil {
					return nil, err
				}
			}
			return handler(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if consumeReset() {
				if inTrailer {
					ss.SetTrailer(resetMD())
				} else if err := ss.SetHeader(resetMD()); err != nil {
					return err
				}
			}
			return handler(srv, ss)
		}),
	)
	pb.RegisterABFSServer(grpcServer, NewPBServer(server))
	go func() {
		_ = grpcServer.Serve(lis)
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
		grpc.WithMaxHeaderListSize(maxHeaderListSize))
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = lis.Close()
	})
	return &PBClient{pb: pb.NewABFSClient(conn)}
}

var testHash = entities.NewHash("a0423896973644771497bdc03eb99d5281615b51")

func shortenResetBackoff(t *testing.T) {
	old := resetInitialBackoff
	resetInitialBackoff = time.Millisecond
	t.Cleanup(func() { resetInitialBackoff = old })
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureResetLog collects the standard log with a fresh limit for the reset lines.
func captureResetLog(t *testing.T) *syncBuffer {
	logs := new(syncBuffer)
	oldLog := resetLog
	resetLog = abfslog.NewLimiter("stream reset retry")
	log.SetOutput(logs)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		resetLog = oldLog
	})
	return logs
}

func blobServer(content []byte, calls *atomic.Int32) *ServerImpl {
	return &ServerImpl{
		GetObjectImpl: func(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
			calls.Add(1)
			return remote.NewContentReader(io.NopCloser(bytes.NewReader(content)), remote.EncodingRaw), nil
		},
	}
}

func TestGetObject_RetriesStreamReset(t *testing.T) {
	shortenResetBackoff(t)
	logs := captureResetLog(t)
	content := []byte("blob content")
	var calls, resets atomic.Int32
	resets.Store(1)
	client := resetPipe(t, blobServer(content, &calls), &resets)

	reader, err := client.GetObject(t.Context(), objects.HashBlobBytes(content))
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, content, got)
	require.Equal(t, int32(2), calls.Load())
	require.Contains(t, logs.String(), "Retrying GetObject after a stream reset (retry 1 of 4): rpc error: code = Internal desc = stream terminated by RST_STREAM with error code: INTERNAL_ERROR")
}

func TestGetObject_StreamResetFailsAfterLimit(t *testing.T) {
	shortenResetBackoff(t)
	content := []byte("blob content")
	var calls, resets atomic.Int32
	resets.Store(100)
	client := resetPipe(t, blobServer(content, &calls), &resets)

	_, err := client.GetObject(t.Context(), objects.HashBlobBytes(content))
	require.Error(t, err)
	require.True(t, isStreamReset(err), "got %v", err)
	require.Equal(t, int32(resetAttempts), calls.Load())
}

func TestGetObject_ServerInternalIsNotRetried(t *testing.T) {
	shortenResetBackoff(t)
	var calls atomic.Int32
	server := &ServerImpl{
		GetObjectImpl: func(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
			calls.Add(1)
			return nil, status.Error(codes.Internal, "backend failed")
		},
	}
	var resets atomic.Int32
	client := resetPipe(t, server, &resets)

	_, err := client.GetObject(t.Context(), testHash)
	require.Equal(t, codes.Internal, status.Code(err))
	require.False(t, isStreamReset(err))
	require.Equal(t, int32(1), calls.Load())
}

// fakeStream returns its replies in order, then io.EOF.
type fakeStream struct {
	grpc.ClientStream
	replies []any
}

func (f *fakeStream) Context() context.Context { return context.Background() }
func (f *fakeStream) SendMsg(m any) error      { return nil }
func (f *fakeStream) CloseSend() error         { return nil }

func (f *fakeStream) Recv() (*pb.GetObjectReply, error) {
	reply := new(pb.GetObjectReply)
	if err := f.RecvMsg(reply); err != nil {
		return nil, err
	}
	return reply, nil
}

func (f *fakeStream) RecvMsg(m any) error {
	if len(f.replies) == 0 {
		return io.EOF
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	if err, ok := r.(error); ok {
		return err
	}
	m.(*pb.GetObjectReply).Chunk = r.(*pb.Chunk)
	return nil
}

// fakeGetObjectClient answers each GetObject call with the next stream.
type fakeGetObjectClient struct {
	pb.ABFSClient
	streams []*fakeStream
}

func (f *fakeGetObjectClient) GetObject(ctx context.Context, in *pb.GetObjectRequest, opts ...grpc.CallOption) (pb.ABFS_GetObjectClient, error) {
	s := f.streams[0]
	f.streams = f.streams[1:]
	return s, nil
}

// failAfter returns err once n bytes were read, which ends the server handler after its first chunks.
// Before that it waits until hold is closed or ctx ends.
type failAfter struct {
	ctx  context.Context
	r    io.Reader
	n    int
	hold chan struct{}
	err  error
}

func (f *failAfter) Read(p []byte) (int, error) {
	if f.n <= 0 {
		select {
		case <-f.hold:
		case <-f.ctx.Done():
		}
		return 0, f.err
	}
	if len(p) > f.n {
		p = p[:f.n]
	}
	k, err := f.r.Read(p)
	f.n -= k
	return k, err
}

func TestGetObject_ResumesAfterStreamResetMidStream(t *testing.T) {
	shortenResetBackoff(t)
	content := make([]byte, MaxMessageSize+5000)
	_, _ = rand.Read(content)
	var calls, resets atomic.Int32
	resets.Store(1)
	// A reset drops the data flow control has not let out yet, so the first call holds the reset until
	// the client has the first chunk.
	received := make(chan struct{})
	server := &ServerImpl{
		GetObjectImpl: func(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
			if calls.Add(1) == 1 {
				reader := &failAfter{ctx: ctx, r: bytes.NewReader(content), n: MaxMessageSize, hold: received, err: errors.New("cut")}
				return remote.NewContentReader(io.NopCloser(reader), remote.EncodingRaw), nil
			}
			return remote.NewContentReader(io.NopCloser(bytes.NewReader(content)), remote.EncodingRaw), nil
		},
	}
	client := trailerResetPipe(t, server, &resets)

	// GetObject returns once the first chunk is in.
	reader, err := client.GetObject(t.Context(), objects.HashBlobBytes(content))
	close(received)
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.True(t, bytes.Equal(content, got), "got %d bytes, want %d", len(got), len(content))
	require.Equal(t, int32(2), calls.Load())
}

func streamReset(code codes.Code, http2Code string) error {
	return status.Error(code, "stream terminated by RST_STREAM with error code: "+http2Code)
}

func TestGetObject_ResumesAtUnalignedOffset(t *testing.T) {
	shortenResetBackoff(t)
	fake := &fakeGetObjectClient{streams: []*fakeStream{
		{replies: []any{&pb.Chunk{Encoding: remote.EncodingRaw, Data: []byte("first ")}, streamReset(codes.Internal, "INTERNAL_ERROR")}},
		// The reopened stream splits the content at another offset.
		{replies: []any{&pb.Chunk{Encoding: remote.EncodingRaw, Data: []byte("fir")}, &pb.Chunk{Data: []byte("st second"), IsLast: true}}},
	}}
	reader, err := (&PBClient{pb: fake}).GetObject(t.Context(), testHash)
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "first second", string(got))
	require.Empty(t, fake.streams)
}

func TestGetObject_RetriesResetDuringSkip(t *testing.T) {
	shortenResetBackoff(t)
	fake := &fakeGetObjectClient{streams: []*fakeStream{
		{replies: []any{&pb.Chunk{Encoding: remote.EncodingRaw, Data: []byte("first ")}, streamReset(codes.Internal, "INTERNAL_ERROR")}},
		{replies: []any{streamReset(codes.Internal, "INTERNAL_ERROR")}},
		{replies: []any{&pb.Chunk{Encoding: remote.EncodingRaw, Data: []byte("first ")}, &pb.Chunk{Data: []byte("second"), IsLast: true}}},
	}}
	reader, err := (&PBClient{pb: fake}).GetObject(t.Context(), testHash)
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "first second", string(got))
	require.Empty(t, fake.streams)
}

func TestGetObject_RetriesProxyCancel(t *testing.T) {
	shortenResetBackoff(t)
	fake := &fakeGetObjectClient{streams: []*fakeStream{
		{replies: []any{streamReset(codes.Canceled, "CANCEL")}},
		{replies: []any{&pb.Chunk{Encoding: remote.EncodingRaw, Data: []byte("content"), IsLast: true}}},
	}}
	reader, err := (&PBClient{pb: fake}).GetObject(t.Context(), testHash)
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "content", string(got))
}

// The messages are the ones grpc-go writes (internal/transport handleRSTStream and ContextErr).
func TestIsStreamReset(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"INTERNAL_ERROR", streamReset(codes.Internal, "INTERNAL_ERROR"), true},
		{"NO_ERROR", streamReset(codes.Internal, "NO_ERROR"), true},
		{"proxy CANCEL", streamReset(codes.Canceled, "CANCEL"), true},
		{"wrapped", fmt.Errorf("failed to reopen stream: %w", streamReset(codes.Internal, "INTERNAL_ERROR")), true},
		{"client cancel", status.Error(codes.Canceled, context.Canceled.Error()), false},
		{"server Internal", status.Error(codes.Internal, "backend failed"), false},
		{"no trailers", status.Error(codes.Internal, "server closed the stream without sending trailers"), false},
		{"not a status", errors.New("stream terminated by RST_STREAM"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isStreamReset(tt.err))
		})
	}
}

func setResetBackoff(t *testing.T, d time.Duration) {
	old := resetInitialBackoff
	resetInitialBackoff = d
	t.Cleanup(func() { resetInitialBackoff = old })
}

func TestGetObject_CancelDuringResetBackoff(t *testing.T) {
	setResetBackoff(t, time.Minute)
	content := []byte("blob content")
	var calls, resets atomic.Int32
	resets.Store(100)
	client := resetPipe(t, blobServer(content, &calls), &resets)

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := client.GetObject(ctx, objects.HashBlobBytes(content))
	require.Equal(t, codes.Canceled, status.Code(err), "got %v", err)
	require.False(t, isStreamReset(err))
	require.Contains(t, err.Error(), "stream terminated by RST_STREAM")
	// The wait would be resetMaxBackoff.
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, int32(1), calls.Load())
}

func TestMapResults_CancelDuringResetBackoff(t *testing.T) {
	setResetBackoff(t, time.Minute)
	var calls, resets atomic.Int32
	resets.Store(100)
	server := &ServerImpl{
		MapResultsImpl: func(ctx context.Context, from remote.Hash) ([]remote.Hash, error) {
			calls.Add(1)
			return nil, nil
		},
	}
	client := resetPipe(t, server, &resets)

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := client.MapResults(ctx, testHash)
	require.Equal(t, codes.Canceled, status.Code(err), "got %v", err)
	require.False(t, isStreamReset(err))
	require.Contains(t, err.Error(), "stream terminated by RST_STREAM")
	// The wait would be resetMaxBackoff.
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, int32(1), calls.Load())
}

func TestGetObject_DeadlineDuringResetBackoff(t *testing.T) {
	setResetBackoff(t, time.Minute)
	content := []byte("blob content")
	var calls, resets atomic.Int32
	resets.Store(100)
	client := resetPipe(t, blobServer(content, &calls), &resets)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := client.GetObject(ctx, objects.HashBlobBytes(content))
	require.Equal(t, codes.DeadlineExceeded, status.Code(err), "got %v", err)
	require.False(t, isStreamReset(err))
	require.Contains(t, err.Error(), "stream terminated by RST_STREAM")
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, int32(1), calls.Load())
}

func TestResetBackoff_Jitter(t *testing.T) {
	bases := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second}
	for i, base := range bases {
		lo, hi := time.Duration(math.MaxInt64), time.Duration(0)
		for range 1000 {
			d := resetBackoff(i + 1)
			lo, hi = min(lo, d), max(hi, d)
		}
		require.GreaterOrEqual(t, lo, base*8/10, "retry %d", i+1)
		require.Less(t, hi, base*12/10, "retry %d", i+1)
		require.Greater(t, hi-lo, base/10, "retry %d", i+1)
	}
}

func TestMapResults_ClientCancelIsNotRetried(t *testing.T) {
	shortenResetBackoff(t)
	var calls, resets atomic.Int32
	server := &ServerImpl{
		MapResultsImpl: func(ctx context.Context, from remote.Hash) ([]remote.Hash, error) {
			calls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	client := resetPipe(t, server, &resets)

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := client.MapResults(ctx, testHash)
	// The plain grpc-go error for a cancelled call: no reset was seen and none was retried.
	require.Equal(t, status.Error(codes.Canceled, context.Canceled.Error()).Error(), err.Error())
	require.Equal(t, int32(1), calls.Load())
}

// A proxy connection reset hits every stream on it at once.
func TestGetObject_ConcurrentStreamResets(t *testing.T) {
	shortenResetBackoff(t)
	logs := captureResetLog(t)
	content := []byte("blob content")
	const readers = 64
	var calls, resets atomic.Int32
	resets.Store(readers)
	client := resetPipe(t, blobServer(content, &calls), &resets)

	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for range readers {
		wg.Go(func() {
			reader, err := client.GetObject(t.Context(), objects.HashBlobBytes(content))
			if err != nil {
				errs <- err
				return
			}
			defer reader.Close()
			got, err := io.ReadAll(reader)
			if err == nil && !bytes.Equal(got, content) {
				err = errors.New("content differs")
			}
			if err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("read failed: %v", err)
	}
	require.Equal(t, int32(2*readers), calls.Load())
	// One line per retry, up to the limit of the process.
	require.Equal(t, 20, strings.Count(logs.String(), "Retrying GetObject after a stream reset"), logs.String())
}

// Health checks call GetVersion and must report a failure at once.
func TestVersion_StreamResetIsNotRetried(t *testing.T) {
	shortenResetBackoff(t)
	var calls, resets atomic.Int32
	resets.Store(1)
	server := &ServerImpl{
		VersionImpl: func(ctx context.Context) (string, error) {
			calls.Add(1)
			return "1.2.3", nil
		},
	}
	client := resetPipe(t, server, &resets)

	_, err := client.Version(t.Context())
	require.True(t, isStreamReset(err), "got %v", err)
	require.Equal(t, int32(1), calls.Load())
}

func TestUnaryReads_RetryStreamReset(t *testing.T) {
	shortenResetBackoff(t)
	logs := captureResetLog(t)
	var calls, resets atomic.Int32
	server := &ServerImpl{
		HeadersImpl: func(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
			calls.Add(1)
			return make([]entities.ObjectHeader, len(keys)), nil
		},
		SHA256Impl: func(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
			calls.Add(1)
			return make([]entities.SHA256, len(keys)), nil
		},
		MapResultsImpl: func(ctx context.Context, from remote.Hash) ([]remote.Hash, error) {
			calls.Add(1)
			return nil, nil
		},
		GetRefsImpl: func(ctx context.Context, server string, project string, refs []string) ([]remote.Hash, error) {
			calls.Add(1)
			return make([]remote.Hash, len(refs)), nil
		},
		SearchRefsImpl: func(ctx context.Context, server string, project string, refPrefix string, limit int) ([]remote.ProjectRefHash, error) {
			calls.Add(1)
			return nil, nil
		},
	}
	client := resetPipe(t, server, &resets)

	ctx := t.Context()
	tests := []struct {
		name string
		call func() error
	}{
		{"Headers", func() error { _, err := client.Headers(ctx, []remote.Hash{testHash}); return err }},
		{"SHA256", func() error { _, err := client.SHA256(ctx, []remote.Hash{testHash}); return err }},
		{"MapResults", func() error { _, err := client.MapResults(ctx, testHash); return err }},
		{"GetRefs", func() error {
			_, err := client.GetRefs(ctx, "server", "project", []string{"refs/heads/main"})
			return err
		}},
		{"SearchRefs", func() error { _, err := client.SearchRefs(ctx, "server", "project", "refs/", 10); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls.Store(0)
			resets.Store(1)
			require.NoError(t, tt.call())
			require.Equal(t, int32(2), calls.Load())
			require.Contains(t, logs.String(), "Retrying "+tt.name+" after a stream reset (retry 1 of 4): rpc error: code = Internal")
		})
	}
}

func TestAddMapResult_RetriesStreamReset(t *testing.T) {
	shortenResetBackoff(t)
	var calls, resets atomic.Int32
	resets.Store(1)
	server := &ServerImpl{
		AddMapResultImpl: func(ctx context.Context, from remote.Hash, to remote.Hash) error {
			calls.Add(1)
			return nil
		},
	}
	client := resetPipe(t, server, &resets)

	from := testHash
	to := entities.NewHash("b0423896973644771497bdc03eb99d5281615b51")
	require.NoError(t, client.AddMapResult(t.Context(), from, to))
	// The server added the result on the reset call too, so the retry adds it again.
	require.Equal(t, int32(2), calls.Load())
}

func TestMapResults_StreamResetFailsAfterLimit(t *testing.T) {
	shortenResetBackoff(t)
	var calls, resets atomic.Int32
	resets.Store(100)
	server := &ServerImpl{
		MapResultsImpl: func(ctx context.Context, from remote.Hash) ([]remote.Hash, error) {
			calls.Add(1)
			return nil, nil
		},
	}
	client := resetPipe(t, server, &resets)

	_, err := client.MapResults(t.Context(), testHash)
	require.True(t, isStreamReset(err), "got %v", err)
	require.Equal(t, int32(resetAttempts), calls.Load())
}

func TestPutRefs_StreamResetIsNotRetried(t *testing.T) {
	shortenResetBackoff(t)
	var calls, resets atomic.Int32
	resets.Store(1)
	server := &ServerImpl{
		PutRefsImpl: func(ctx context.Context, server string, project string, refs []remote.RefHash) error {
			calls.Add(1)
			return nil
		},
	}
	client := resetPipe(t, server, &resets)

	err := client.PutRefs(t.Context(), "server", "project", []remote.RefHash{{Ref: "refs/heads/main"}})
	require.True(t, isStreamReset(err), "got %v", err)
	require.Equal(t, int32(1), calls.Load())
}
