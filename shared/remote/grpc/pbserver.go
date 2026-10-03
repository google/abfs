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
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
)

var putObjectRequestPool = sync.Pool{
	New: func() any {
		return new(pb.PutObjectRequest)
	},
}

func getPutObjectRequest() Chunker {
	req := putObjectRequestPool.Get().(*pb.PutObjectRequest)
	req.Reset()
	return req
}

func putPutObjectRequest(c Chunker) {
	if req, ok := c.(*pb.PutObjectRequest); ok {
		putObjectRequestPool.Put(req)
	}
}

type PBServer struct {
	pb.UnimplementedABFSServer
	server remote.RemoteProvider
}

func NewPBServer(server remote.RemoteProvider) *PBServer {
	return &PBServer{
		server: server,
	}
}

var _ pb.ABFSServer = (*PBServer)(nil)

// Headers implements ABFSServer.
func (s *PBServer) Headers(ctx context.Context, req *pb.HeadersRequest) (*pb.HeadersReply, error) {
	keys := HashesFromInputBytes(req.Keys)
	headers, err := s.server.Headers(ctx, keys)
	if err != nil {
		return nil, err
	}
	pbHeaders := make([]*pb.ObjectHeader, len(headers))
	flatHeaders := make([]pb.ObjectHeader, len(headers))
	for i, header := range headers {
		flatHeaders[i] = pb.ObjectHeader{Type: string(header.Type), Size: header.Size}
		pbHeaders[i] = &flatHeaders[i]
	}
	return &pb.HeadersReply{Headers: pbHeaders}, nil
}

// SHA256 implements ABFSServer.
func (s *PBServer) SHA256(ctx context.Context, req *pb.SHA256Request) (*pb.SHA256Reply, error) {
	keys := HashesFromInputBytes(req.Keys)
	shas, err := s.server.SHA256(ctx, keys)
	if err != nil {
		return nil, err
	}

	return &pb.SHA256Reply{Sha256: OutputBytesFromSHA256(shas)}, nil
}

// GetObject implements ABFSServer.
func (s *PBServer) GetObject(req *pb.GetObjectRequest, stream pb.ABFS_GetObjectServer) (retErr error) {
	ctx := stream.Context()
	reader, err := s.server.GetObject(ctx, HashFromInputBytes(req.Key))
	if err != nil {
		if entities.IsNotExist(err) { // Covers remote.ErrNotFound too
			return status.Error(codes.NotFound, "object not found on the server")
		}

		return err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	return copyToStream(reader, stream, nil)
}

// PutObject implements ABFSServer.
func (s *PBServer) PutObject(stream pb.ABFS_PutObjectServer) error {
	reader, err := newSimpleRecvStream(stream, getPutObjectRequest, putPutObjectRequest)
	if err != nil {
		return err
	}

	err = s.server.PutObject(stream.Context(), reader.key, reader)
	if err != nil {
		return WrapErrorInStatus(err)
	}
	return stream.SendAndClose(&pb.PutObjectReply{})
}

// MapResults implements ABFSServer.
func (s *PBServer) MapResults(ctx context.Context, req *pb.MapResultsRequest) (*pb.MapResultsReply, error) {
	key := HashFromInputBytes(req.Id)
	results, err := s.server.MapResults(ctx, key)
	if err != nil {
		return nil, err
	}
	return &pb.MapResultsReply{Results: OutputBytesFromHashes(results)}, nil
}

// AddMapResult implements ABFSServer.
func (s *PBServer) AddMapResult(ctx context.Context, req *pb.AddMapResultRequest) (*pb.AddMapResultReply, error) {
	from := HashFromInputBytes(req.From)
	to := HashFromInputBytes(req.To)
	err := s.server.AddMapResult(ctx, from, to)
	if err != nil {
		return nil, WrapErrorInStatus(err)
	}
	return &pb.AddMapResultReply{}, nil
}

// GetRefs implements ABFSServer.
func (s *PBServer) GetRefs(ctx context.Context, req *pb.GetRefsRequest) (*pb.GetRefsReply, error) {
	hashes, err := s.server.GetRefs(ctx, req.Server, req.Project, req.Refs)
	if entities.IsNotExist(err) {
		return nil, status.Errorf(codes.NotFound, "the server %q not found", req.Server)
	}
	if err != nil {
		// Currently, these errors don't need wrapped in a gRPC status because we just log and don't return an error
		// if there is permission denied.
		return nil, err
	}
	return &pb.GetRefsReply{Hashes: OutputBytesFromHashes(hashes)}, nil
}

// SearchRefs implements ABFSServer.
func (s *PBServer) SearchRefs(ctx context.Context, req *pb.SearchRefsRequest) (*pb.SearchRefsReply, error) {
	result, err := s.server.SearchRefs(ctx, req.Server, req.Project, req.RefPrefix, int(req.Limit))
	if err != nil {
		// Currently, these errors don't need wrapped in a gRPC status because we just log and don't return an error
		// if there is permission denied.
		return nil, err
	}

	refs := make([]*pb.ProjectRefHash, len(result))
	flatRefs := make([]pb.ProjectRefHash, len(result))
	flatHashes := make([]byte, len(result)*20)
	for i, rh := range result {
		start := i * 20
		end := start + 20
		copy(flatHashes[start:end], rh.Hash[:])

		flatRefs[i] = pb.ProjectRefHash{
			Project: rh.Project,
			Ref:     rh.Ref,
			Hash:    flatHashes[start:end],
		}
		refs[i] = &flatRefs[i]
	}

	return &pb.SearchRefsReply{Result: refs}, nil
}

// PutRefs implements ABFSServer.
func (s *PBServer) PutRefs(ctx context.Context, req *pb.PutRefsRequest) (*pb.PutRefsReply, error) {
	refs := make([]entities.RefHash, len(req.Refs))
	for i, rh := range req.Refs {
		refs[i] = entities.RefHash{
			Ref:  rh.Ref,
			Hash: HashFromInputBytes(rh.Hash),
		}
	}

	if err := s.server.PutRefs(ctx, req.Server, req.Project, refs); err != nil {
		return nil, WrapErrorInStatus(err)
	}
	return &pb.PutRefsReply{}, nil
}

// GetVersion implements ABFSServer.
func (s *PBServer) GetVersion(ctx context.Context, req *pb.GetVersionRequest) (*pb.GetVersionReply, error) {
	version, err := s.server.Version(ctx)
	if err != nil {
		return nil, err
	}
	return &pb.GetVersionReply{Version: version}, nil
}

// WhoAmI implements ABFSServer.
// This call does not get delegated to the s.server RemoteProvider
// because it is not a function of the backing store. One implementation
// can be used.
func (s *PBServer) WhoAmI(ctx context.Context, req *pb.WhoAmIRequest) (*pb.WhoAmIReply, error) {
	user, ok := remote.UserFromContext(ctx)
	if !ok {
		user = remote.User{
			Email: "unknown",
		}
	}

	return &pb.WhoAmIReply{
		Sub:           user.Subject,
		Email:         user.Email,
		EmailVerified: true,
	}, nil
}
