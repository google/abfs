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
	"strings"

	"github.com/mwitkow/grpc-proxy/proxy"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type ConnectionManager interface {
	Select() (*grpc.ClientConn, error)
}

// ProxyDirector returns a function that is intended to be used with the "github.com/mwitkow/grpc-proxy/proxy" package.
// Example:
//
//	grpc.NewServer(grpc.UnknownServiceHandler(proxy.TransparentHandler(abfsgrpc.ProxyDirector(pb.ABFS_ServiceDesc.ServiceName, proxyPrefixToConnection))))
func ProxyDirector(mainServiceName string, proxyPrefixToConnManager map[string]ConnectionManager) proxy.StreamDirector {
	// based on example on https://github.com/mwitkow/grpc-proxy/blob/master/proxy/examples_test.go
	return func(ctx context.Context, fullMethodName string) (context.Context, grpc.ClientConnInterface, error) {
		// fullMethodName is like /ares.ARES/Execute
		parts := strings.Split(fullMethodName, "/")
		if len(parts) < 1 {
			return nil, nil, status.Errorf(codes.Unimplemented, "Unknown method %q", fullMethodName)
		}
		service := parts[1]

		// Make sure we never forward methods for this service
		if service == mainServiceName {
			return nil, nil, status.Errorf(codes.Unimplemented, "Unknown method %q", fullMethodName)
		}

		md, ok := metadata.FromIncomingContext(ctx)
		outCtx := metadata.NewOutgoingContext(ctx, md.Copy())

		if ok {
			manager, ok := proxyPrefixToConnManager[service]
			if !ok {
				return nil, nil, status.Errorf(codes.Unimplemented, "Unknown service %q", service)
			}
			conn, err := manager.Select()
			if err != nil {
				return nil, nil, status.Errorf(codes.Unavailable, "Error with connection for service %q: %v", service, err)
			}
			return outCtx, conn, nil
		}
		return nil, nil, status.Errorf(codes.Unimplemented, "Unknown method %q", fullMethodName)
	}
}
