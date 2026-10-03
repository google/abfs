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

package remote

import (
	"context"
	"io"

	"github.com/google/abfs/shared/entities"
)

// CompressingProvider wraps a RemoteProvider and handles compression/decompression.
type CompressingProvider struct {
	RemoteProvider
}

// NewCompressingProvider creates a new CompressingProvider.
func NewCompressingProvider(remote RemoteProvider) *CompressingProvider {
	return &CompressingProvider{
		RemoteProvider: remote,
	}
}

// GetObject implements RemoteProvider.
func (c *CompressingProvider) GetObject(ctx context.Context, key Hash) (ContentReadCloser, error) {
	reader, err := c.RemoteProvider.GetObject(ctx, key)
	if err != nil {
		return nil, err
	}
	newReader, err := DecodeToRaw(reader)
	if err != nil {
		_ = reader.Close()
		return nil, err
	}

	return newReader, nil
}

// PutObject implements RemoteProvider.
func (c *CompressingProvider) PutObject(ctx context.Context, key Hash, reader ContentReadCloser) (retErr error) {
	r, err := EncodeToZStd(reader)
	if err != nil {
		return err
	}

	defer entities.CloseErrCheck(r, &retErr)
	return c.RemoteProvider.PutObject(ctx, key, r)
}

// CompressingCloser wraps a RemoteCloser and handles compression/decompression.
type CompressingCloser struct {
	*CompressingProvider
	closer io.Closer
}

// NewCompressingCloser creates a new CompressingCloser.
func NewCompressingCloser(remote RemoteCloser) *CompressingCloser {
	return &CompressingCloser{
		CompressingProvider: NewCompressingProvider(remote),
		closer:              remote,
	}
}

func (c *CompressingCloser) Close() error {
	return c.closer.Close()
}
