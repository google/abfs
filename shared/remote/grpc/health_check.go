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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"

	"github.com/google/abfs/shared/remote/servers/auth"
)

func validateServerVersion(header metadata.MD, serverMinVersion string) error {
	serverVersion := "0.0.0"
	if len(header["server-version"]) > 0 {
		serverVersion = header["server-version"][0]
	}
	if semver.Compare("v"+serverVersion, "v"+serverMinVersion) < 0 {
		return fmt.Errorf("server version (%s) is less than minimum supported version (%s)", serverVersion, serverMinVersion)
	}
	return nil
}

func clientMetaInterceptor(serviceName, clientVersion, serverMinVersion string) grpc.UnaryClientInterceptor {
	servicePrefix := "/" + serviceName
	return func(
		ctx context.Context,
		method string,
		req interface{},
		reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		clientMeta := metadata.Pairs("client-version", clientVersion)
		ctx = metadata.NewOutgoingContext(ctx, clientMeta)

		var serverMeta metadata.MD
		opts = append(opts, grpc.Header(&serverMeta))
		err := invoker(ctx, method, req, reply, cc, opts...)
		if err != nil {
			return err
		}
		// Skip version checking the server on health check.
		// Once new server versions are deployed that don't check client version, we can stop sending the client-version (above) on health-checks also.
		if !strings.HasPrefix(method, servicePrefix) {
			return nil
		}
		if err := validateServerVersion(serverMeta, serverMinVersion); err != nil {
			return err
		}
		return nil
	}
}

func clientMetaStreamInterceptor(serviceName, clientVersion, serverMinVersion string) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		clientMeta := metadata.Pairs("client-version", clientVersion)
		ctx = metadata.NewOutgoingContext(ctx, clientMeta)

		cs, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			return nil, err
		}

		// TODO: after all servers are upgraded and sending their version with stream responses, we can enable this version check.
		//
		// // Skip version checking the server on health check.
		// // Once new server versions are deployed that don't check client version, we can stop sending the client-version (above) on health-checks also.
		// servicePrefix := "/" + serviceName
		// if !strings.HasPrefix(method, servicePrefix) {
		// 	return cs, nil
		// }

		// // Wait for header to get server version
		// header, err := cs.Header()
		// if err != nil {
		// 	// If we can't get header, it might be an error or just closed.
		// 	// Usually Header() returns error if the stream fails before headers are received.
		// 	return nil, err
		// }

		// if err := validateServerVersion(header, serverMinVersion); err != nil {
		// 	return nil, err
		// }

		return cs, nil
	}
}

// GrpcDialOpts contains the options for GrpcDialHealthy.
type GrpcDialOpts struct {
	ServiceName      string
	Address          string
	BytesStatHandler BytesStatHandler
	ClientVersion    string
	ServerMinVersion string
	TokenSource      auth.TokenSource
	RootCAFile       string
	RetryPolicy      string
	DisableTLS       bool
	SkipHealthCheck  bool
}

type tokenSourceCreds struct {
	ts         auth.TokenSource
	requireTLS bool
}

func (t tokenSourceCreds) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	token, err := t.ts.Token()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"authorization": token.Type() + " " + token.AccessToken,
	}, nil
}

func (t tokenSourceCreds) RequireTransportSecurity() bool {
	return t.requireTLS
}

// GrpcDialHealthy is like grpc.Dial but returns error on an unhealthy connection
func GrpcDialHealthy(o *GrpcDialOpts) (*grpc.ClientConn, error) {
	byteCounter := &ByteCounter{o.BytesStatHandler}

	opts := []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(
			clientMetaInterceptor(o.ServiceName, o.ClientVersion, o.ServerMinVersion),
		),
		grpc.WithStreamInterceptor(clientMetaStreamInterceptor(o.ServiceName, o.ClientVersion, o.ServerMinVersion)),
		grpc.WithStatsHandler(byteCounter),
	}

	if o.RetryPolicy != "" {
		opts = append(opts, grpc.WithDefaultServiceConfig(o.RetryPolicy))
	}

	var creds credentials.TransportCredentials
	if o.DisableTLS {
		creds = insecure.NewCredentials()
	} else if o.RootCAFile == "" {
		// Use default system trust store
		creds = credentials.NewClientTLSFromCert(nil, "")
	} else {
		// Use provided CA (useful for testing or internal CAs)
		certBytes, err := os.ReadFile(o.RootCAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read root CA file: %v", err)
		}
		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(certBytes) {
			return nil, fmt.Errorf("failed to add root CA to pool")
		}
		creds = credentials.NewTLS(&tls.Config{
			ServerName: strings.Split(o.Address, ":")[0], // Extract hostname from address
			RootCAs:    certPool,
		})
	}
	opts = append(opts, grpc.WithTransportCredentials(creds))

	if o.TokenSource != nil {
		perRPCAuth := tokenSourceCreds{
			ts:         o.TokenSource,
			requireTLS: !o.DisableTLS,
		}
		opts = append(opts, grpc.WithPerRPCCredentials(perRPCAuth))
	}

	conn, err := grpc.NewClient(o.Address, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial address %s: %v", o.Address, err)
	}

	if !o.SkipHealthCheck {
		if err := HealthCheck(conn, o.ServiceName); err != nil {
			if err := conn.Close(); err != nil {
				log.Printf("Failed to close connection manager: %v", err)
			}
			return nil, fmt.Errorf("error during health check: %w", err)
		}
	}

	return conn, nil
}

func HealthCheck(conn *grpc.ClientConn, service string) error {
	cli := grpc_health_v1.NewHealthClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := &grpc_health_v1.HealthCheckRequest{Service: service}
	result, err := cli.Check(ctx, req)

	if err != nil {
		return err
	}
	if result.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		return fmt.Errorf("not healthy, status was %s", result.Status)
	}
	return nil
}

type BytesStatHandler interface {
	AddBytesSent(int64)
	AddBytesReceived(int64)
}

type ByteCounter struct {
	bs BytesStatHandler
}

func (b *ByteCounter) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return ctx
}

func (b *ByteCounter) HandleRPC(ctx context.Context, s stats.RPCStats) {
	switch st := s.(type) {
	case *stats.OutPayload:
		b.bs.AddBytesSent(int64(st.Length))
	case *stats.InPayload:
		b.bs.AddBytesReceived(int64(st.Length))
	}
}

func (b *ByteCounter) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	return ctx
}

func (b *ByteCounter) HandleConn(ctx context.Context, s stats.ConnStats) {}
