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
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/auth/credentials/idtoken"
	"github.com/jellydator/ttlcache/v3"
	"github.com/mwitkow/grpc-proxy/proxy"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/remote"
	abfsgrpc "github.com/google/abfs/shared/remote/grpc"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
	"github.com/google/abfs/shared/remote/servers"
)

type tokenValidationResult struct {
	payload *idtoken.Payload
	err     error
}

var (
	tokenCache               *ttlcache.Cache[string, tokenValidationResult]
	tokenCacheOnce           sync.Once
	tokenFlightGroup         singleflight.Group
	underlyingTokenValidator = defaultTokenValidator
	tokenValidator           = cachedTokenValidator
	timeNow                  = time.Now
)

func initTokenCache() {
	tokenCache = ttlcache.New(
		ttlcache.WithCapacity[string, tokenValidationResult](10000),
		ttlcache.WithDisableTouchOnHit[string, tokenValidationResult](),
	)
	go tokenCache.Start()
}

func defaultTokenValidator(ctx context.Context, token string) (*idtoken.Payload, error) {
	return idtoken.Validate(ctx, token, "")
}

func cachedTokenValidator(ctx context.Context, token string) (*idtoken.Payload, error) {
	tokenCacheOnce.Do(initTokenCache)

	if item := tokenCache.Get(token); item != nil {
		res := item.Value()
		return res.payload, res.err
	}

	val, err, _ := tokenFlightGroup.Do(token, func() (interface{}, error) {
		if item := tokenCache.Get(token); item != nil {
			return item.Value(), nil
		}

		payload, err := underlyingTokenValidator(ctx, token)
		res := tokenValidationResult{payload: payload, err: err}

		var ttl time.Duration
		if err == nil {
			now := timeNow()
			expiresAt := time.Unix(payload.Expires, 0)
			ttl = expiresAt.Sub(now)
			if ttl <= 0 {
				return res, nil
			}
			if ttl > time.Hour {
				ttl = time.Hour
			}
		} else {
			ttl = 5 * time.Second
		}

		tokenCache.Set(token, res, ttl)
		return res, nil
	})

	if err != nil {
		return nil, err
	}

	res := val.(tokenValidationResult)
	return res.payload, res.err
}

func validateToken(ctx context.Context) (*idtoken.Payload, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.Unauthenticated, "missing metadata")
	}

	authHeaders := md.Get("authorization")
	if len(authHeaders) == 0 {
		return nil, status.Errorf(codes.Unauthenticated, "missing authorization header")
	}

	authHeader := authHeaders[0]
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, status.Errorf(codes.Unauthenticated, "invalid authorization header format")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		return nil, status.Errorf(codes.Unauthenticated, "empty token")
	}

	payload, err := tokenValidator(ctx, token)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
	}

	email, _ := payload.Claims["email"].(string)
	if email == "" {
		return nil, status.Errorf(codes.Unauthenticated, "missing email claim in token")
	}

	verified, _ := payload.Claims["email_verified"].(bool)
	if !verified {
		return nil, status.Errorf(codes.Unauthenticated, "email %q is not verified", email)
	}

	return payload, nil
}

func authInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
		return handler(ctx, req)
	}
	payload, err := validateToken(ctx)
	if err != nil {
		return nil, err
	}
	email, _ := payload.Claims["email"].(string)
	user := remote.User{
		Email:   email,
		Subject: payload.Subject,
	}
	ctx = remote.NewContextWithUser(ctx, user)

	return handler(ctx, req)
}

func authStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
		return handler(srv, ss)
	}
	payload, err := validateToken(ss.Context())
	if err != nil {
		return err
	}
	email, _ := payload.Claims["email"].(string)
	user := remote.User{
		Email:   email,
		Subject: payload.Subject,
	}
	ctx := remote.NewContextWithUser(ss.Context(), user)

	return handler(srv, &headerServerStream{ServerStream: ss, ctx: ctx})
}

// headerServerStream wraps grpc.ServerStream to replace the context.
type headerServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the wrapped context.
func (h *headerServerStream) Context() context.Context {
	return h.ctx
}

// GRPCServerOpts contains the options for creating a new gRPC server.
type GRPCServerOpts struct {
	Server                   remote.RemoteCloser
	ProxyPrefixToConnManager map[string]abfsgrpc.ConnectionManager
	MaxConnectionAge         time.Duration

	// The server will setup TLS credentials
	// and authentication interceptors if these
	// values are set.
	// This requires *a* TLS termination to occur at the server,
	// but not necessarily the one from the client.
	// Either of these approaches:
	// 1. client <-TLS-> server
	// 2. client <-TLS-> load balancer <-TLS-> server
	// While unencrypted traffic from LB to the server is possible, this
	// is discouraged for security purposes, even inside internal VPCs.
	CertFile string
	KeyFile  string

	// AuthEnabled allows enabling authentication interceptors.
	AuthEnabled bool

	ExtraServerOpts []grpc.ServerOption
}

// NewGRPCServer creates a new gRPC server with the given options.
func NewGRPCServer(o *GRPCServerOpts) (*grpc.Server, error) {
	var serverOpts []grpc.ServerOption
	var unaryInterceptors []grpc.UnaryServerInterceptor
	var streamInterceptors []grpc.StreamServerInterceptor

	// Always add version metadata interceptor
	unaryInterceptors = append(unaryInterceptors, abfsgrpc.ServerVersionMetadataInterceptor(pb.ABFS_ServiceDesc.ServiceName, servers.ClientMinVersion, servers.ServerVersion))
	streamInterceptors = append(streamInterceptors, abfsgrpc.ServerVersionMetadataStreamInterceptor(pb.ABFS_ServiceDesc.ServiceName, servers.ClientMinVersion, servers.ServerVersion))

	serverOpts = append(serverOpts, o.ExtraServerOpts...)

	needTLS := o.CertFile != "" && o.KeyFile != ""
	if needTLS {
		creds, err := credentials.NewServerTLSFromFile(o.CertFile, o.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS credentials: %v", err)
		}
		serverOpts = append(serverOpts, grpc.Creds(creds))
		fmt.Println("TLS enabled.")
	}

	needAuth := o.AuthEnabled
	if needAuth {
		unaryInterceptors = append(unaryInterceptors, authInterceptor)
		streamInterceptors = append(streamInterceptors, authStreamInterceptor)
		fmt.Println("Auth interceptors enabled.")
	}

	// Add the chained interceptors to server options
	if len(unaryInterceptors) > 0 {
		serverOpts = append(serverOpts, grpc.ChainUnaryInterceptor(unaryInterceptors...))
	}
	if len(streamInterceptors) > 0 {
		serverOpts = append(serverOpts, grpc.ChainStreamInterceptor(streamInterceptors...))
	}

	if o.MaxConnectionAge > 0 {
		serverOpts = append(serverOpts, grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionAge:      o.MaxConnectionAge,
			MaxConnectionAgeGrace: 60 * time.Minute,
		}))
	}

	hs := health.NewServer()
	if len(o.ProxyPrefixToConnManager) > 0 {
		serverOpts = append(serverOpts, grpc.UnknownServiceHandler(proxy.TransparentHandler(abfsgrpc.ProxyDirector(pb.ABFS_ServiceDesc.ServiceName, o.ProxyPrefixToConnManager))))
		for serv := range o.ProxyPrefixToConnManager {
			// TODO: this should adapt to to the current state because there could be 0 healthy connections
			hs.SetServingStatus(serv, grpc_health_v1.HealthCheckResponse_SERVING)
		}
	}
	s := grpc.NewServer(serverOpts...)
	pbServer := abfsgrpc.NewPBServer(o.Server)
	pb.RegisterABFSServer(s, pbServer)
	hs.SetServingStatus(pb.ABFS_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(s, hs)
	// helps with debugging, so calls like `grpc_cli ls` can work
	reflection.Register(s)
	return s, nil
}
