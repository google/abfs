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
	"net"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/auth/credentials/idtoken"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/remote"
	pb "github.com/google/abfs/shared/remote/grpc/proto"
)

func TestAuthInterceptor_HealthCheck(t *testing.T) {
	info := &grpc.UnaryServerInfo{
		FullMethod: "/grpc.health.v1.Health/Check",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}

	resp, err := authInterceptor(context.Background(), nil, info, handler)
	assert.NoError(t, err)
	assert.Equal(t, "ok", resp)
}

func TestAuthInterceptor_NonHealthCheck(t *testing.T) {
	info := &grpc.UnaryServerInfo{
		FullMethod: "/abfs.ABFS/WhoAmI",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}

	_, err := authInterceptor(context.Background(), nil, info, handler)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

func TestAuthStreamInterceptor_HealthCheck(t *testing.T) {
	info := &grpc.StreamServerInfo{
		FullMethod: "/grpc.health.v1.Health/Watch",
	}
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		return nil
	}

	ss := &mockServerStream{ctx: context.Background()}
	err := authStreamInterceptor(nil, ss, info, handler)
	assert.NoError(t, err)
}

func TestAuthStreamInterceptor_NonHealthCheck(t *testing.T) {
	info := &grpc.StreamServerInfo{
		FullMethod: "/abfs.ABFS/StreamHeaders",
	}
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		return nil
	}

	ss := &mockServerStream{ctx: context.Background()}
	err := authStreamInterceptor(nil, ss, info, handler)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

type mockRemoteCloser struct {
	remote.RemoteProvider
}

func (m mockRemoteCloser) Close() error {
	return nil
}

func TestNewGRPCServer_AuthWithoutTLS(t *testing.T) {
	// 1. Save and restore tokenValidator
	oldValidator := tokenValidator
	defer func() { tokenValidator = oldValidator }()

	// Mock token validator to accept various test tokens
	tokenValidator = func(ctx context.Context, token string) (*idtoken.Payload, error) {
		switch token {
		case "valid-token":
			return &idtoken.Payload{
				Claims: map[string]interface{}{
					"email":          "test@example.com",
					"email_verified": true,
				},
				Subject: "test-sub",
			}, nil
		case "unverified-email-token":
			return &idtoken.Payload{
				Claims: map[string]interface{}{
					"email":          "test@example.com",
					"email_verified": false,
				},
				Subject: "test-sub",
			}, nil
		case "missing-verified-claim-token":
			return &idtoken.Payload{
				Claims: map[string]interface{}{
					"email": "test@example.com",
				},
				Subject: "test-sub",
			}, nil
		case "non-bool-verified-claim-token":
			return &idtoken.Payload{
				Claims: map[string]interface{}{
					"email":          "test@example.com",
					"email_verified": "true",
				},
				Subject: "test-sub",
			}, nil
		case "missing-email-token":
			return &idtoken.Payload{
				Claims: map[string]interface{}{
					"email_verified": true,
				},
				Subject: "test-sub",
			}, nil
		case "nil-claims-token":
			return &idtoken.Payload{
				Claims:  nil,
				Subject: "test-sub",
			}, nil
		default:
			return nil, fmt.Errorf("invalid token")
		}
	}

	// 2. Start a server with AuthEnabled: true, but no TLS
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = lis.Close() }()

	mockRemote := &mockRemoteCloser{}

	s, err := NewGRPCServer(&GRPCServerOpts{
		Server:      mockRemote,
		AuthEnabled: true,
	})
	require.NoError(t, err)
	go func() {
		_ = s.Serve(lis)
	}()
	defer s.GracefulStop()

	// 3. Connect to the server
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	client := pb.NewABFSClient(conn)

	// 4. Try to call WhoAmI without token (should fail)
	ctxNoToken := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("client-version", "0.1.2"))
	_, err = client.WhoAmI(ctxNoToken, &pb.WhoAmIRequest{})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())

	// 5. Try to call WhoAmI with invalid token (should fail)
	ctxInvalid := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"client-version", "0.1.2",
		"authorization", "Bearer invalid-token",
	))
	_, err = client.WhoAmI(ctxInvalid, &pb.WhoAmIRequest{})
	require.Error(t, err)
	st, ok = status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())

	// 6. Test various invalid/missing claims (all should return Unauthenticated without panicking)
	invalidClaimTokens := []string{
		"unverified-email-token",
		"missing-verified-claim-token",
		"non-bool-verified-claim-token",
		"missing-email-token",
		"nil-claims-token",
	}
	for _, token := range invalidClaimTokens {
		ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
			"client-version", "0.1.2",
			"authorization", "Bearer "+token,
		))
		_, err = client.WhoAmI(ctx, &pb.WhoAmIRequest{})
		require.Error(t, err, "expected error for token %s", token)
		st, ok = status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.Unauthenticated, st.Code(), "expected Unauthenticated for token %s", token)
	}

	// 7. Try to call WhoAmI with valid token (should succeed)
	ctxValid := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"client-version", "0.1.2",
		"authorization", "Bearer valid-token",
	))
	resp, err := client.WhoAmI(ctxValid, &pb.WhoAmIRequest{})
	require.NoError(t, err)
	assert.Equal(t, "test@example.com", resp.Email)
	assert.Equal(t, "test-sub", resp.Sub)
}

func TestTokenValidator_SuccessCacheHit(t *testing.T) {
	// Force initialization and reset cache
	_, _ = cachedTokenValidator(context.Background(), "init-token")
	tokenCache.DeleteAll()

	// Track underlying calls
	callCount := 0
	oldValidator := underlyingTokenValidator
	defer func() { underlyingTokenValidator = oldValidator }()

	underlyingTokenValidator = func(ctx context.Context, token string) (*idtoken.Payload, error) {
		callCount++
		if token == "success-token" {
			return &idtoken.Payload{
				Expires: time.Now().Add(10 * time.Minute).Unix(),
				Subject: "user-123",
				Claims: map[string]interface{}{
					"email":          "user@example.com",
					"email_verified": true,
				},
			}, nil
		}
		return nil, fmt.Errorf("invalid token")
	}

	// First call -> Cache Miss
	payload, err := cachedTokenValidator(context.Background(), "success-token")
	require.NoError(t, err)
	assert.Equal(t, "user-123", payload.Subject)
	assert.Equal(t, 1, callCount)

	// Second call -> Cache Hit
	payload2, err := cachedTokenValidator(context.Background(), "success-token")
	require.NoError(t, err)
	assert.Equal(t, "user-123", payload2.Subject)
	assert.Equal(t, 1, callCount) // callCount must NOT have changed!
}

func TestTokenValidator_FailureCacheHit(t *testing.T) {
	_, _ = cachedTokenValidator(context.Background(), "init-token")
	tokenCache.DeleteAll()

	callCount := 0
	oldValidator := underlyingTokenValidator
	defer func() { underlyingTokenValidator = oldValidator }()

	underlyingTokenValidator = func(ctx context.Context, token string) (*idtoken.Payload, error) {
		callCount++
		return nil, fmt.Errorf("signature verification failed")
	}

	// First call -> Cache Miss (failure)
	_, err := cachedTokenValidator(context.Background(), "bad-token")
	require.Error(t, err)
	assert.Equal(t, 1, callCount)

	// Second call -> Cache Hit (cached failure result)
	_, err2 := cachedTokenValidator(context.Background(), "bad-token")
	require.Error(t, err2)
	assert.Equal(t, 1, callCount) // callCount must NOT have changed!
}

func TestTokenValidator_CacheTTLAndExpiry(t *testing.T) {
	_, _ = cachedTokenValidator(context.Background(), "init-token")
	tokenCache.DeleteAll()

	// Setup our timeNow override mock
	baseTime := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	mockedTime := baseTime
	oldTimeNow := timeNow
	defer func() { timeNow = oldTimeNow }()
	timeNow = func() time.Time {
		return mockedTime
	}

	oldValidator := underlyingTokenValidator
	defer func() { underlyingTokenValidator = oldValidator }()

	// We'll return payloads with varying dynamic expirations
	var mockExpires int64
	var mockErr error

	underlyingTokenValidator = func(ctx context.Context, token string) (*idtoken.Payload, error) {
		if mockErr != nil {
			return nil, mockErr
		}
		return &idtoken.Payload{
			Expires: mockExpires,
			Subject: "test-user",
		}, nil
	}

	// 1. Scenario A: Token with 10 minutes of life remaining.
	mockExpires = baseTime.Add(10 * time.Minute).Unix()
	mockErr = nil
	tokenCache.DeleteAll()

	_, err := cachedTokenValidator(context.Background(), "token-10m")
	require.NoError(t, err)
	item := tokenCache.Get("token-10m")
	require.NotNil(t, item)
	// Cache item's Expiration time on the real clock should be set relative to Now (i.e., +10 minutes)
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), item.ExpiresAt(), 2*time.Second)

	// 2. Scenario B: Token with 2 hours of life remaining (should be capped at 1 hour maximum).
	mockExpires = baseTime.Add(2 * time.Hour).Unix()
	tokenCache.DeleteAll()

	_, err = cachedTokenValidator(context.Background(), "token-2h")
	require.NoError(t, err)
	item = tokenCache.Get("token-2h")
	require.NotNil(t, item)
	// Cache item's Expiration should be capped to exactly 1 hour on real clock
	assert.WithinDuration(t, time.Now().Add(1*time.Hour), item.ExpiresAt(), 2*time.Second)

	// 3. Scenario C: Failed validation (should negative-cache for 5 seconds).
	mockErr = fmt.Errorf("signature verification failed")
	tokenCache.DeleteAll()

	_, err = cachedTokenValidator(context.Background(), "token-fail")
	require.Error(t, err)
	item = tokenCache.Get("token-fail")
	require.NotNil(t, item)
	// Expiration should be 5 seconds
	assert.WithinDuration(t, time.Now().Add(5*time.Second), item.ExpiresAt(), 2*time.Second)

	// 4. Scenario D: Already expired token (should not be cached).
	mockErr = nil
	mockExpires = baseTime.Add(-10 * time.Minute).Unix() // expired 10 minutes ago
	tokenCache.DeleteAll()

	_, err = cachedTokenValidator(context.Background(), "token-expired")
	require.NoError(t, err)
	item = tokenCache.Get("token-expired")
	assert.Nil(t, item) // Expired tokens should NOT be cached at all!
}

func TestTokenValidator_SingleflightStampede(t *testing.T) {
	_, _ = cachedTokenValidator(context.Background(), "init-token")
	tokenCache.DeleteAll()

	callCount := 0
	oldValidator := underlyingTokenValidator
	defer func() { underlyingTokenValidator = oldValidator }()

	// We block the mock validator for 50ms so all concurrent requests are in flight together
	underlyingTokenValidator = func(ctx context.Context, token string) (*idtoken.Payload, error) {
		callCount++
		time.Sleep(50 * time.Millisecond)
		return &idtoken.Payload{
			Expires: time.Now().Add(10 * time.Minute).Unix(),
			Subject: "shared-user",
		}, nil
	}

	const concurrentWorkers = 20
	var wg sync.WaitGroup
	wg.Add(concurrentWorkers)

	results := make([]*idtoken.Payload, concurrentWorkers)
	errors := make([]error, concurrentWorkers)

	for i := 0; i < concurrentWorkers; i++ {
		go func(idx int) {
			defer wg.Done()
			p, err := cachedTokenValidator(context.Background(), "heavy-token")
			results[idx] = p
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// Ensure all concurrent workers received successful responses with identical subject
	for idx := 0; idx < concurrentWorkers; idx++ {
		require.NoError(t, errors[idx])
		require.NotNil(t, results[idx])
		assert.Equal(t, "shared-user", results[idx].Subject)
	}

	// Verify the underlying validator was executed EXACTLY once
	assert.Equal(t, 1, callCount)
}
