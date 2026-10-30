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
	"io"
	"log"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
)

var chunkBufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, MaxMessageSize)
		return &buf
	},
}

type Stream interface {
	Context() context.Context
	SendMsg(m any) error
	RecvMsg(m any) error
}

type recvStream struct {
	key            entities.Hash
	encoding       string
	stream         Stream
	openStream     func() (Stream, error)
	ctx            context.Context
	clock          *OutageClock
	reached        bool
	newChunker     func() Chunker
	releaseChunker func(Chunker)
	currChunker    Chunker
	last           bool
	buf            []byte
	totalBytesRead int64
}

type Chunker interface {
	GetChunk() *pb.Chunk
}

var _ remote.ContentReadCloser = (*recvStream)(nil)

func newSimpleRecvStream(stream Stream, newChunker func() Chunker, releaseChunker func(Chunker)) (*recvStream, error) {
	return newRecvStream(context.Background(), nil, stream, nil, newChunker, releaseChunker)
}

// newResumableRecvStream opens a stream that is reopened when it is cut. With an enabled clock, opening
// and reopening wait for an unreachable server.
func newResumableRecvStream(ctx context.Context, clock *OutageClock, openStream func() (Stream, error), newChunker func() Chunker, releaseChunker func(Chunker)) (*recvStream, error) {
	return newRecvStream(ctx, clock, nil, openStream, newChunker, releaseChunker)
}

func newRecvStream(ctx context.Context, clock *OutageClock, stream Stream, openStream func() (Stream, error), newChunker func() Chunker, releaseChunker func(Chunker)) (*recvStream, error) {
	s := &recvStream{
		stream:         stream,
		openStream:     openStream,
		ctx:            ctx,
		clock:          clock,
		newChunker:     newChunker,
		releaseChunker: releaseChunker,
	}

	if s.stream == nil && s.openStream != nil {
		if s.clock.gaveUp() {
			return nil, s.clock.unreachable(ctx, errTransientFailure)
		}
		var err error
		s.stream, err = s.openStream()
		if err != nil && s.clock.enabled() && status.Code(err) == codes.Unavailable {
			err = s.reopenWithWait(err)
		}
		if err != nil {
			return nil, err
		}
	} else if s.stream == nil {
		return nil, errors.New("neither stream nor openStream provided")
	}

	if err := s.receive(); err != nil {
		if err := s.Close(); err != nil {
			log.Printf("Failed to close stream: %v", err)
		}
		return nil, err
	}
	return s, nil
}

// TODO close in a separate goroutine for performance reasons.

// Close implements remote.ContentReadCloser.
func (s *recvStream) Close() error {
	if s.releaseChunker != nil && s.currChunker != nil {
		s.releaseChunker(s.currChunker)
		s.currChunker = nil
	}
	if closer, isCloser := s.stream.(interface {
		CloseSend() error
	}); isCloser {
		return closer.CloseSend()
	}
	return nil
}

// Encoding implements remote.ContentReadCloser.
func (s *recvStream) Encoding() string {
	return s.encoding
}

// Read implements remote.ContentReadCloser.
func (s *recvStream) Read(p []byte) (n int, err error) {
	n = 0
	for len(p) > 0 {
		if len(s.buf) == 0 {
			if s.last {
				return n, io.EOF
			}
			if err := s.receive(); err != nil {
				return n, err
			}
		}
		i := copy(p, s.buf)
		p = p[i:]
		s.buf = s.buf[i:]
		n += i
		s.totalBytesRead += int64(i)
	}
	return n, nil
}

func (s *recvStream) receive() error {
	for {
		if len(s.buf) > 0 {
			return nil
		}

		if s.releaseChunker != nil && s.currChunker != nil {
			s.releaseChunker(s.currChunker)
			s.currChunker = nil
		}

		chunker := s.newChunker()
		err := s.stream.RecvMsg(chunker)
		if err == nil {
			if !s.reached {
				s.clock.Reached()
				s.reached = true
			}
			chunk := chunker.GetChunk()
			if len(chunk.Key) > 0 {
				s.key = HashFromInputBytes(chunk.Key)
			}
			if chunk.Encoding != "" {
				s.encoding = chunk.Encoding
			}
			if chunk.IsLast {
				s.last = true
			}
			s.buf = chunk.Data
			s.currChunker = chunker
			return nil
		}

		if s.releaseChunker != nil {
			s.releaseChunker(chunker)
		}

		// Check if retryable
		code := status.Code(err)
		shouldRetry := code == codes.Unavailable || code == codes.ResourceExhausted || (err == io.EOF && !s.last)

		if !shouldRetry || s.openStream == nil {
			return rewrapNotFound(err)
		}

		if s.clock.enabled() {
			if err := s.reopenWithWait(err); err != nil {
				return err
			}
		} else {
			// Retry logic
			time.Sleep(100 * time.Millisecond) // Simple backoff
			if err := s.reopenAndSkip(); err != nil {
				return err
			}
		}
		if len(s.buf) > 0 || s.last {
			return nil
		}
	}
}

// reopenWithWait reopens a cut stream and skips what was read, trying again while the clock lets
// calls wait for the server.
func (s *recvStream) reopenWithWait(cause error) error {
	time.Sleep(100 * time.Millisecond)
	for {
		if s.clock.gaveUp() {
			return s.clock.unreachable(s.ctx, cause)
		}
		s.reached = false
		err := s.reopenAndSkip()
		if err == nil {
			return nil
		}
		// Only an unreachable server is worth waiting for; other errors keep the single reopen.
		if status.Code(err) != codes.Unavailable {
			return err
		}
		cause = err
		if !s.clock.waitForServer(s.ctx) {
			return s.clock.unreachable(s.ctx, cause)
		}
	}
}

func (s *recvStream) reopenAndSkip() error {
	if s.openStream == nil {
		return errors.New("cannot reopen stream: no factory provided")
	}
	// Close old stream if possible
	if closer, ok := s.stream.(interface{ CloseSend() error }); ok {
		if err := closer.CloseSend(); err != nil {
			return fmt.Errorf("failed to close stream: %w", err)
		}
	}

	newStream, err := s.openStream()
	if err != nil {
		return fmt.Errorf("failed to reopen stream: %w", err)
	}
	s.stream = newStream

	toSkip := s.totalBytesRead
	for toSkip > 0 {
		chunker := s.newChunker()
		if err := s.stream.RecvMsg(chunker); err != nil {
			if s.releaseChunker != nil {
				s.releaseChunker(chunker)
			}
			return fmt.Errorf("failed to skip bytes during stream resumption: %w", err)
		}
		if !s.reached {
			s.clock.Reached()
			s.reached = true
		}
		chunk := chunker.GetChunk()

		if len(chunk.Key) > 0 {
			s.key = HashFromInputBytes(chunk.Key)
		}
		if chunk.Encoding != "" {
			if s.encoding != "" && chunk.Encoding != s.encoding {
				if s.releaseChunker != nil {
					s.releaseChunker(chunker)
				}
				// The bytes already read are in the old encoding, so the rest cannot follow them.
				return fmt.Errorf("cannot resume stream: encoding changed from %q to %q", s.encoding, chunk.Encoding)
			}
			s.encoding = chunk.Encoding
		}

		n := int64(len(chunk.Data))
		if n > toSkip {
			s.buf = chunk.Data[toSkip:]
			if chunk.IsLast {
				s.last = true
			}
			s.currChunker = chunker
			return nil
		}
		if s.releaseChunker != nil {
			s.releaseChunker(chunker)
		}
		toSkip -= n
		if chunk.IsLast && toSkip > 0 {
			return errors.New("stream ended before skipping required bytes")
		}
		if chunk.IsLast {
			s.last = true
			return nil
		}
	}
	return nil
}

func copyToStream(reader remote.ContentReadCloser, stream Stream, key *remote.Hash) error {
	enc := reader.Encoding()

	var keyBytes []byte
	if key != nil {
		keyBytes = HashToBytes(*key)
	}

	bufPtr := chunkBufferPool.Get().(*[]byte)
	defer chunkBufferPool.Put(bufPtr)
	buf := *bufPtr

	isLast := false
	for !isLast {
		n, err := io.ReadAtLeast(reader, buf, len(buf))
		isLast = err == io.EOF || err == io.ErrUnexpectedEOF
		if err != nil && !isLast {
			return err
		}
		err = stream.SendMsg(&pb.PutObjectRequest{
			Chunk: &pb.Chunk{
				Encoding: enc,
				Key:      keyBytes,
				IsLast:   isLast,
				Data:     buf[:n],
			},
		})
		if err != nil {
			return err
		}
		enc = ""
		keyBytes = nil
	}
	return nil
}
