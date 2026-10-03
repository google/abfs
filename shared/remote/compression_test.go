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
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestCompressingProvider_GetObject(t *testing.T) {
	ctx := context.Background()
	hash := entities.NewHash("aaaabbbbccccddddeeeeffff0000111122223333")
	content := []byte("hello world")

	// Create compressed content
	compressedReader := NewZStdContentReader(content)
	compressedContent, err := io.ReadAll(compressedReader)
	require.NoError(t, err)

	mock := &mockRemoteCloser{
		getObject: func(ctx context.Context, key Hash) (ContentReadCloser, error) {
			// Serve compressed content
			return NewContentReader(io.NopCloser(bytes.NewReader(compressedContent)), EncodingZStd), nil
		},
	}

	provider := NewCompressingProvider(mock)

	// GetObject should return decompressed content
	r, err := provider.GetObject(ctx, hash)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, r.Close())
	}()

	require.Equal(t, EncodingRaw, r.Encoding())

	readContent, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, content, readContent)
}

func TestCompressingProvider_PutObject(t *testing.T) {
	ctx := context.Background()
	hash := entities.NewHash("aaaabbbbccccddddeeeeffff0000111122223333")
	content := []byte("hello world")

	var uploadedContent []byte
	var uploadedEncoding string

	mock := &mockRemoteCloser{
		putObject: func(ctx context.Context, key Hash, reader ContentReadCloser) error {
			uploadedEncoding = reader.Encoding()
			var err error
			uploadedContent, err = io.ReadAll(reader)
			return err
		},
	}

	provider := NewCompressingProvider(mock)

	err := provider.PutObject(ctx, hash, NewContentReader(io.NopCloser(bytes.NewReader(content)), EncodingRaw))
	require.NoError(t, err)

	require.Equal(t, EncodingZStd, uploadedEncoding)

	// Verify uploaded content is actually compressed
	r := NewContentReader(io.NopCloser(bytes.NewReader(uploadedContent)), EncodingZStd)
	decompressedR, err := DecodeToRaw(r)
	require.NoError(t, err)
	decompressedContent, err := io.ReadAll(decompressedR)
	require.NoError(t, err)

	require.Equal(t, content, decompressedContent)
}
