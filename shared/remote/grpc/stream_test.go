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
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/google/abfs/shared/remote/grpc/proto"
)

// scriptedStream serves chunks and fails with err after cutAfter chunks (never if cutAfter < 0).
type scriptedStream struct {
	chunks   []*pb.Chunk
	cutAfter int
	err      error
	served   int
}

func (s *scriptedStream) Context() context.Context { return context.Background() }
func (s *scriptedStream) SendMsg(any) error        { return nil }
func (s *scriptedStream) RecvMsg(m any) error {
	if s.cutAfter >= 0 && s.served == s.cutAfter {
		return s.err
	}
	if s.served >= len(s.chunks) {
		return io.EOF
	}
	m.(*pb.GetObjectReply).Chunk = s.chunks[s.served]
	s.served++
	return nil
}

// opener returns the scripted results in turn: an error, or a stream.
type opener struct {
	results []any
	opens   int
}

func (o *opener) open() (Stream, error) {
	r := o.results[min(o.opens, len(o.results)-1)]
	o.opens++
	if err, ok := r.(error); ok {
		return nil, err
	}
	return r.(*scriptedStream), nil
}

func testChunks(encoding string) ([]*pb.Chunk, []byte) {
	var chunks []*pb.Chunk
	var all []byte
	for i := range 4 {
		data := bytes.Repeat([]byte{byte('a' + i)}, 1024)
		all = append(all, data...)
		chunks = append(chunks, &pb.Chunk{Encoding: encoding, Data: data, IsLast: i == 3})
	}
	return chunks, all
}

func stream(chunks []*pb.Chunk, cutAfter int) *scriptedStream {
	return &scriptedStream{chunks: chunks, cutAfter: cutAfter, err: unavailable()}
}

func readAll(t *testing.T, clock *OutageClock, o *opener) ([]byte, error) {
	return readAllCtx(t, context.Background(), clock, o)
}

func readAllCtx(t *testing.T, ctx context.Context, clock *OutageClock, o *opener) ([]byte, error) {
	s, err := newResumableRecvStream(ctx, clock, o.open, getGetObjectReply, putGetObjectReply)
	if err != nil {
		return nil, err
	}
	defer func() { require.NoError(t, s.Close()) }()
	return io.ReadAll(s)
}

func TestRecvStream_ResumesAcrossOutage(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	chunks, want := testChunks("zstd")
	o := &opener{results: []any{stream(chunks, 2), unavailable(), unavailable(), unavailable(), stream(chunks, -1)}}

	clock := NewOutageClock(time.Second)
	got, err := readAll(t, clock, o)

	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, 5, o.opens)
	require.False(t, clock.Waiting())
}

func TestRecvStream_OpenWaitsForServer(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	chunks, want := testChunks("zstd")
	o := &opener{results: []any{unavailable(), unavailable(), stream(chunks, -1)}}

	got, err := readAll(t, NewOutageClock(time.Second), o)

	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestRecvStream_GivesUpAfterTimeout(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	chunks, _ := testChunks("zstd")
	o := &opener{results: []any{stream(chunks, 1), unavailable()}}

	_, err := readAll(t, NewOutageClock(100*time.Millisecond), o)

	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Contains(t, err.Error(), "server unreachable for")
	require.Greater(t, o.opens, 2)
}

func TestRecvStream_EncodingChangedOnResume(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	zstdChunks, _ := testChunks("zstd")
	rawChunks, _ := testChunks("raw")
	o := &opener{results: []any{stream(zstdChunks, 2), stream(rawChunks, -1)}}

	_, err := readAll(t, NewOutageClock(time.Second), o)

	require.ErrorContains(t, err, "encoding changed")
}

func TestRecvStream_WithoutClockReopensOnce(t *testing.T) {
	chunks, _ := testChunks("zstd")
	o := &opener{results: []any{stream(chunks, 1), unavailable(), stream(chunks, -1)}}

	_, err := readAll(t, nil, o)

	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Equal(t, 2, o.opens)
}

func TestRecvStream_OtherErrorsReopenOnce(t *testing.T) {
	withPause(t, 10*time.Millisecond)
	chunks, _ := testChunks("zstd")
	exhausted := status.Error(codes.ResourceExhausted, "message too large")
	o := &opener{results: []any{&scriptedStream{chunks: chunks, cutAfter: 1, err: exhausted}, exhausted}}

	_, err := readAll(t, NewOutageClock(time.Minute), o)

	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, 2, o.opens)
}

func TestRecvStream_ContextCanceledWhileWaiting(t *testing.T) {
	withPause(t, time.Minute)
	chunks, _ := testChunks("zstd")
	o := &opener{results: []any{stream(chunks, 1), unavailable()}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := readAllCtx(t, ctx, NewOutageClock(time.Hour), o)

	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second)
}
