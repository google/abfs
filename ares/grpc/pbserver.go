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
	"log"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/google/abfs/ares/grpc/proto"
	"github.com/google/abfs/ares/servers"
	abfsgrpc "github.com/google/abfs/shared/remote/grpc"
)

// PBServer serves to convert between gRPC and golang conventions
type PBServer struct {
	pb.UnimplementedARESServer
	server *servers.ARESServer
}

func NewPBServer(server *servers.ARESServer) *PBServer {
	return &PBServer{
		server: server,
	}
}

var _ pb.ARESServer = (*PBServer)(nil)

func (s *PBServer) Execute(ctx context.Context, req *pb.ExecuteRequest) (*pb.ExecuteResponse, error) {
	h := abfsgrpc.HashFromInputBytes(req.EvalRequest)
	patches := abfsgrpc.HashesFromInputBytes(req.Patches)

	resp, err := s.server.Execute(ctx, h, patches, req.StoreMapResult, req.PrefetchInputs)
	if err != nil {
		log.Printf("ERROR: returning codes.Internal error from Execute for req %q, patches %s: %v", h, patches, err)
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.ExecuteResponse{
		EvalResponse: abfsgrpc.HashToBytes(resp),
	}, nil
}
