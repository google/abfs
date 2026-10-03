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

package ari

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	"github.com/bazelbuild/remote-apis-sdks/go/pkg/rexec"

	"github.com/google/abfs/shared/remote"
)

// BlobClient defines the interface for fetching ARI blob objects.
type BlobClient interface {
	Close() error
	FetchBlob(ctx context.Context, ari string) (remote.ContentReadCloser, error)
}

// Client fetches ARI blob objects from RBE.
type Client struct {
	rbeClient *rexec.Client
}

var _ BlobClient = (*Client)(nil)

// NewClient creates a new ARI Client.
func NewClient(rbeClient *rexec.Client) *Client {
	return &Client{
		rbeClient: rbeClient,
	}
}

// FetchBlob parses an ARI string in the form of 'rbe:sha256:size'
// and fetches the blob bytes directly from RBE.
func (c *Client) FetchBlob(ctx context.Context, ari string) (remote.ContentReadCloser, error) {
	if !strings.HasPrefix(ari, "rbe:") {
		return nil, fmt.Errorf("invalid ARI prefix in %q: expected 'rbe:' prefix", ari)
	}

	parts := strings.Split(ari, ":")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid ARI format %q: expected 'rbe:sha256:size'", ari)
	}

	sha256Hex := parts[1]
	sizeStr := parts[2]

	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil || size < 0 {
		return nil, fmt.Errorf("invalid size %q in ARI %q: %w", sizeStr, ari, err)
	}

	rbeDigest := digest.Digest{
		Hash: sha256Hex,
		Size: size,
	}

	blob, _, err := c.rbeClient.GrpcClient.ReadBlob(ctx, rbeDigest)
	if err != nil {
		return nil, fmt.Errorf("RBE fetch failed for ARI %s (SHA256: %s, size: %d): %w", ari, sha256Hex, size, err)
	}

	return remote.NewReaderFromBytes(blob), nil
}

// Close closes the underlying RBE client connection.
func (c *Client) Close() error {
	if c.rbeClient != nil && c.rbeClient.GrpcClient != nil {
		return c.rbeClient.GrpcClient.Close()
	}
	return nil
}
