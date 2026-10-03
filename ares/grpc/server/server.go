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

package server

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	aresgrpc "github.com/google/abfs/ares/grpc"
	pb "github.com/google/abfs/ares/grpc/proto"
	"github.com/google/abfs/ares/servers"
	abfsgrpc "github.com/google/abfs/shared/remote/grpc"
)

// NewGRPCServer serves to accept the golang implementation and wrap it in the proper
// gRPC necessities.
func NewGRPCServer(server *servers.ARESServer, maxConnectionAge time.Duration) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(abfsgrpc.ServerVersionMetadataInterceptor(pb.ARES_ServiceDesc.ServiceName, servers.ClientMinVersion, servers.ServerVersion)),
	}
	if maxConnectionAge > 0 {
		opts = append(opts, grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionAge:      maxConnectionAge,
			MaxConnectionAgeGrace: 60 * time.Minute,
		}))
	}
	s := grpc.NewServer(opts...)
	pbServer := aresgrpc.NewPBServer(server)
	pb.RegisterARESServer(s, pbServer)
	hs := health.NewServer()
	hs.SetServingStatus(pb.ARES_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(s, hs)
	// helps with debugging, so calls like `grpc_cli ls` can work
	reflection.Register(s)
	return s
}
