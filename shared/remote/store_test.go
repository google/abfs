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
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

type mockRemoteCloser struct {
	getObject func(ctx context.Context, key Hash) (ContentReadCloser, error)
	putObject func(ctx context.Context, key Hash, reader ContentReadCloser) error
}

func (m *mockRemoteCloser) GetObject(ctx context.Context, key Hash) (ContentReadCloser, error) {
	if m.getObject != nil {
		return m.getObject(ctx, key)
	}
	return nil, entities.NotExistErrf("cannot find object %s", key)
}

func (m *mockRemoteCloser) PutObject(ctx context.Context, key Hash, reader ContentReadCloser) error {
	if m.putObject != nil {
		return m.putObject(ctx, key, reader)
	}
	return nil
}

func (m *mockRemoteCloser) Headers(ctx context.Context, keys []Hash) ([]entities.ObjectHeader, error) {
	return nil, nil
}
func (m *mockRemoteCloser) SHA256(ctx context.Context, keys []Hash) ([]entities.SHA256, error) {
	return nil, nil
}
func (m *mockRemoteCloser) MapResults(ctx context.Context, from Hash) ([]Hash, error) {
	return nil, nil
}
func (m *mockRemoteCloser) AddMapResult(ctx context.Context, from, to Hash) error { return nil }
func (m *mockRemoteCloser) GetRefs(ctx context.Context, server string, project string, refs []string) ([]Hash, error) {
	return nil, nil
}
func (m *mockRemoteCloser) SearchRefs(ctx context.Context, server string, project string, refPrefix string, limit int) ([]ProjectRefHash, error) {
	return nil, nil
}
func (m *mockRemoteCloser) PutRefs(ctx context.Context, server string, project string, refs []RefHash) error {
	return nil
}
func (m *mockRemoteCloser) Mark(ctx context.Context, hash Hash, key, value string) (int64, error) {
	return 0, nil
}
func (m *mockRemoteCloser) Version(ctx context.Context) (string, error) { return "", nil }
func (m *mockRemoteCloser) WhoAmI(ctx context.Context) (string, error)  { return "", nil }
func (m *mockRemoteCloser) Close() error                                { return nil }

func TestRemoteStore_Read(t *testing.T) {
	ctx := context.Background()
	hash := entities.NewHash("aaaabbbbccccddddeeeeffff0000111122223333")
	content := []byte("blob 12\x00hello world!")

	tests := []struct {
		name       string
		compressed bool
	}{
		{name: "Compressed", compressed: true},
		{name: "Uncompressed", compressed: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mock RemoteCloser = &mockRemoteCloser{
				getObject: func(ctx context.Context, key Hash) (ContentReadCloser, error) {
					if tc.compressed {
						return NewZStdContentReader(content), nil
					}
					return NewContentReader(io.NopCloser(bytes.NewReader(content)), EncodingRaw), nil
				},
			}

			if tc.compressed {
				mock = NewCompressingCloser(mock)
			}

			store := NewRemoteStore(mock)

			// Test OpenBlob
			// OpenBlob expects the standard Git object header "type size\0"
			r, err := store.OpenBlob(ctx, hash)
			require.NoError(t, err)
			data, err := io.ReadAll(r)
			require.NoError(t, err)
			require.NoError(t, r.Close())
			// OpenBlob consumes the header, so we expect "hello world!"
			require.Equal(t, "hello world!", string(data))

			// Test GetObject
			// GetObject returns the full content including header
			data, err = store.GetObject(ctx, hash)
			require.NoError(t, err)
			require.Equal(t, content, data)

			// Test OpenObjectFile
			// OpenObjectFile returns the full content including header (as a reader)
			r, err = store.OpenObjectFile(ctx, hash)
			require.NoError(t, err)
			data, err = io.ReadAll(r)
			require.NoError(t, err)
			require.NoError(t, r.Close())
			require.Equal(t, content, data)
		})
	}
}

func TestRemoteStore_Write(t *testing.T) {
	ctx := context.Background()
	hash := entities.NewHash("aaaabbbbccccddddeeeeffff0000111122223333")
	content := []byte("hello world!")

	tests := []struct {
		name       string
		compressed bool
	}{
		{name: "Compressed", compressed: true},
		{name: "Uncompressed", compressed: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var capturedData []byte
			var capturedEncoding string
			var mock RemoteCloser = &mockRemoteCloser{
				putObject: func(ctx context.Context, key Hash, reader ContentReadCloser) error {
					capturedEncoding = reader.Encoding()
					var err error
					capturedData, err = io.ReadAll(reader)
					return err
				},
			}

			if tc.compressed {
				mock = NewCompressingCloser(mock)
			}

			store := NewRemoteStore(mock)

			// Test WriteBlob
			// WriteBlob adds the blob header
			err := store.WriteBlob(ctx, hash, int64(len(content)), bytes.NewReader(content))
			require.NoError(t, err)

			expectedBlobContent := []byte(fmt.Sprintf("blob %d\x00%s", len(content), content))

			if tc.compressed {
				require.Equal(t, EncodingZStd, capturedEncoding)
				// Decompress capturedData to verify it matches expectedBlobContent
				r := NewContentReader(io.NopCloser(bytes.NewReader(capturedData)), EncodingZStd)
				decoded, err := DecodeToRaw(r)
				require.NoError(t, err)
				decodedData, err := io.ReadAll(decoded)
				require.NoError(t, err)
				require.Equal(t, expectedBlobContent, decodedData)
			} else {
				require.Equal(t, EncodingRaw, capturedEncoding)
				require.Equal(t, expectedBlobContent, capturedData)
			}

			// Test WriteObject
			// WriteObject writes raw content
			capturedData = nil // reset
			err = store.WriteObject(ctx, hash, bytes.NewReader(content))
			require.NoError(t, err)

			if tc.compressed {
				require.Equal(t, EncodingZStd, capturedEncoding)
				r := NewContentReader(io.NopCloser(bytes.NewReader(capturedData)), EncodingZStd)
				decoded, err := DecodeToRaw(r)
				require.NoError(t, err)
				decodedData, err := io.ReadAll(decoded)
				require.NoError(t, err)
				require.Equal(t, content, decodedData)
			} else {
				require.Equal(t, EncodingRaw, capturedEncoding)
				require.Equal(t, content, capturedData)
			}
		})
	}
}
