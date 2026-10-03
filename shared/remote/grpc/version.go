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
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func validateVersion(clientVersion, clientMinVersion string) error {
	if semver.Compare("v"+clientVersion, "v"+clientMinVersion) < 0 {
		return fmt.Errorf("client version (%s) is less than minimum supported version (%s)", clientVersion, clientMinVersion)
	}
	return nil
}

func checkClientVersion(ctx context.Context, fullMethod, servicePrefix, clientMinVersion string) error {
	// Only do client version check if method is directed at our service.
	// We might be proxying to another service or handling the health check.
	if !strings.HasPrefix(fullMethod, servicePrefix) {
		return nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Errorf(codes.Unauthenticated, "missing metadata")
	}

	clientVersion := "0.0.0"
	if len(md["client-version"]) > 0 {
		clientVersion = md["client-version"][0]
	}

	if err := validateVersion(clientVersion, clientMinVersion); err != nil {
		return status.Errorf(codes.PermissionDenied, "client version invalid: %s", err)
	}
	return nil
}

// ServerVersionMetadataInterceptor is meant to be used with grpc.UnaryInterceptor.
// Example:
//
//	grpc.NewServer(
//	 grpc.UnaryInterceptor(abfsgrpc.ServerVersionMetadataInterceptor(pb.ARES_ServiceDesc.ServiceName)),
//	)
func ServerVersionMetadataInterceptor(serviceName, clientMinVersion, serverVersion string) grpc.UnaryServerInterceptor {
	servicePrefix := "/" + serviceName
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		if err := checkClientVersion(ctx, info.FullMethod, servicePrefix, clientMinVersion); err != nil {
			return nil, err
		}

		resp, err := handler(ctx, req)

		if strings.HasPrefix(info.FullMethod, servicePrefix) {
			header := metadata.Pairs("server-version", serverVersion)
			if err := grpc.SendHeader(ctx, header); err != nil {
				return nil, err
			}
		}

		return resp, err
	}
}

// ServerVersionMetadataStreamInterceptor is meant to be used with grpc.StreamInterceptor.
// Example:
//
//	grpc.NewServer(
//	 grpc.StreamInterceptor(abfsgrpc.ServerVersionMetadataStreamInterceptor(pb.ARES_ServiceDesc.ServiceName)),
//	)
func ServerVersionMetadataStreamInterceptor(serviceName, clientMinVersion, serverVersion string) grpc.StreamServerInterceptor {
	servicePrefix := "/" + serviceName
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if err := checkClientVersion(ss.Context(), info.FullMethod, servicePrefix, clientMinVersion); err != nil {
			return err
		}

		if strings.HasPrefix(info.FullMethod, servicePrefix) {
			header := metadata.Pairs("server-version", serverVersion)
			if err := ss.SendHeader(header); err != nil {
				return err
			}
		}

		return handler(srv, ss)
	}
}
