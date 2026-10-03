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
	context "context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
)

var getObjectReplyPool = sync.Pool{
	New: func() any {
		return new(pb.GetObjectReply)
	},
}

func getGetObjectReply() Chunker {
	resp := getObjectReplyPool.Get().(*pb.GetObjectReply)
	resp.Reset()
	return resp
}

func putGetObjectReply(c Chunker) {
	if resp, ok := c.(*pb.GetObjectReply); ok {
		getObjectReplyPool.Put(resp)
	}
}

const (
	maxHeaderLen   = 50000
	MaxMessageSize = int(pb.ServiceConstants_MAX_CHUNK_BYTES)
	// observed refs averaged 52 bytes, so using 53 for overhead
	putRefsPerMessageHeuristic = MaxMessageSize / 53
)

type PBClient struct {
	pb pb.ABFSClient
}

func NewPBClient(pb pb.ABFSClient) *PBClient {
	return &PBClient{
		pb: pb,
	}
}

var _ remote.RemoteProvider = (*PBClient)(nil)

// Headers implements remote.RemoteProvider.
func (c *PBClient) Headers(ctx context.Context, keys []remote.Hash) ([]entities.ObjectHeader, error) {
	headers := make([]entities.ObjectHeader, 0, len(keys))

	for subKeys := range slices.Chunk(keys, maxHeaderLen) {
		resp, err := c.pb.Headers(ctx, &pb.HeadersRequest{Keys: HashesToHashBytes(subKeys)})
		if err != nil {
			return nil, err
		}
		for _, hdr := range resp.Headers {
			headers = append(headers, entities.ObjectHeader{
				Type: entities.ObjectType(hdr.Type),
				Size: hdr.Size,
			})
		}
	}
	return headers, nil
}

// SHA256 implements remote.RemoteProvider.
func (c *PBClient) SHA256(ctx context.Context, keys []remote.Hash) ([]entities.SHA256, error) {
	shas := make([]entities.SHA256, 0, len(keys))

	for subKeys := range slices.Chunk(keys, maxHeaderLen) {
		resp, err := c.pb.SHA256(ctx, &pb.SHA256Request{Keys: HashesToHashBytes(subKeys)})
		if err != nil {
			return nil, err
		}
		shas = append(shas, SHA256BytesToSHA256(resp.Sha256)...)
	}
	return shas, nil
}

// GetObject implements remote.RemoteProvider.
func (c *PBClient) GetObject(ctx context.Context, key remote.Hash) (remote.ContentReadCloser, error) {
	openStream := func() (Stream, error) {
		return c.pb.GetObject(ctx, &pb.GetObjectRequest{Key: HashToBytes(key)})
	}
	s, err := newResumableRecvStream(openStream, getGetObjectReply, putGetObjectReply)
	if err != nil {
		return nil, rewrapNotFound(err)
	}
	return s, nil
}

// PutObject implements remote.RemoteProvider.
func (c *PBClient) PutObject(ctx context.Context, key remote.Hash, reader remote.ContentReadCloser) error {
	for {
		stream, err := c.pb.PutObject(ctx)
		if err != nil {
			return UnwrapMissingLinkError(err)
		}

		err = copyToStream(reader, stream, &key)
		if errors.Is(err, io.EOF) {
			// Server short-circuits the stream if it already has the content, or if there was an error.
			// We continue to CloseAndRecv to get the actual status.
			err = nil
		}

		if err == nil {
			_, err = stream.CloseAndRecv()
			return UnwrapMissingLinkError(err)
		}

		// Check for retry
		code := status.Code(err)
		shouldRetry := code == codes.Unavailable || code == codes.ResourceExhausted

		if !shouldRetry {
			return UnwrapMissingLinkError(err)
		}

		// Retry logic
		if seeker, ok := reader.(io.Seeker); ok {
			if _, seekErr := seeker.Seek(0, io.SeekStart); seekErr != nil {
				// Cannot seek, cannot retry
				return UnwrapMissingLinkError(err)
			}
			// Seek success, retry loop
			continue
		} else {
			// Reader not seekable
			return UnwrapMissingLinkError(err)
		}
	}
}

// MapResults implements remote.RemoteProvider.
func (c *PBClient) MapResults(ctx context.Context, from remote.Hash) ([]remote.Hash, error) {
	r, err := c.pb.MapResults(ctx, &pb.MapResultsRequest{Id: from[:]})
	if err != nil {
		return nil, err
	}
	return HashBytesToHashes(r.GetResults()), nil
}

// AddMapResult implements remote.RemoteProvider.
func (c *PBClient) AddMapResult(ctx context.Context, from remote.Hash, to remote.Hash) error {
	// TODO: can map entries be larger than 4 MB? Then need to use a slower streaming function
	_, err := c.pb.AddMapResult(ctx, &pb.AddMapResultRequest{From: from[:], To: to[:]})
	return UnwrapMissingLinkError(err)
}

// GetRefs implements remote.RemoteProvider.
func (c *PBClient) GetRefs(ctx context.Context, server string, project string, refs []string) ([]remote.Hash, error) {
	result, err := c.pb.GetRefs(ctx, &pb.GetRefsRequest{
		Server:  server,
		Project: project,
		Refs:    refs,
	})
	if err != nil {
		return nil, rewrapNotFound(err)
	}
	return HashBytesToHashes(result.Hashes), nil
}

func rewrapNotFound(err error) error {
	if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
		return entities.NotExistErrf("%s", s.Message())
	}
	return err
}

// SearchRefs implements remote.RemoteProvider.
func (c *PBClient) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]remote.ProjectRefHash, error) {
	refs, err := c.pb.SearchRefs(ctx, &pb.SearchRefsRequest{
		Server:    server,
		Project:   project,
		RefPrefix: refPrefix,
		Limit:     int32(limit),
	})
	if err != nil {
		return nil, rewrapNotFound(err)
	}

	result := make([]entities.ProjectRefHash, len(refs.Result))
	for i, rh := range refs.Result {
		result[i] = entities.ProjectRefHash{
			Project: rh.Project,
			Ref:     rh.Ref,
			Hash:    HashFromInputBytes(rh.Hash),
		}
	}
	return result, nil
}

// PutRefs implements remote.RemoteProvider.
func (c *PBClient) PutRefs(ctx context.Context, server string, project string, refs []remote.RefHash) error {
	pbRefs := make([]*pb.RefHash, len(refs))
	flatRefs := make([]pb.RefHash, len(refs))
	flatHashes := make([]byte, len(refs)*20)
	for i, rh := range refs {
		start := i * 20
		end := start + 20
		copy(flatHashes[start:end], rh.Hash[:])

		flatRefs[i] = pb.RefHash{
			Ref:  rh.Ref,
			Hash: flatHashes[start:end],
		}
		pbRefs[i] = &flatRefs[i]
	}
	// each ref could be a different size and the message has overhead, so we start with this guess
	// and adjust down if necessary
	refsPerMessage := putRefsPerMessageHeuristic
	for len(pbRefs) > 0 {
		end := refsPerMessage
		if end > len(pbRefs) {
			end = len(pbRefs)
		}
		req := &pb.PutRefsRequest{
			Server:  server,
			Project: project,
			Refs:    pbRefs[:end],
		}
		if proto.Size(req) > MaxMessageSize {
			// try 95% of messages next time
			// dividing first to avoid overflow if max increases later
			// and it should be sufficiently large to avoid hitting 0
			refsPerMessage = (refsPerMessage / 100) * 95
			continue
		}
		_, err := c.pb.PutRefs(ctx, req)
		if err != nil {
			return UnwrapMissingLinkError(err)
		}
		pbRefs = pbRefs[end:]
	}

	return nil
}

// Mark implements remote.RemoteProvider.
func (c *PBClient) Mark(ctx context.Context, hash remote.Hash, key string, value string) (int64, error) {
	panic("unimplemented")
}

// Version implements remote.RemoteProvider.
func (c *PBClient) Version(ctx context.Context) (string, error) {
	reply, err := c.pb.GetVersion(ctx, &pb.GetVersionRequest{})
	if err != nil {
		return "", err
	}
	return reply.Version, nil
}

// WhoAmI implements remote.RemoteProvider.
func (c *PBClient) WhoAmI(ctx context.Context) (string, error) {
	reply, err := c.pb.WhoAmI(ctx, &pb.WhoAmIRequest{})
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(reply)
	if err != nil {
		return "", nil
	}
	return string(b), nil
}
