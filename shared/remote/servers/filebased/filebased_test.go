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

package filebased

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	_ "github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
)

// TODO Make these tests more generic so that we run them against all server implementations.

func Setup(t *testing.T) (*fileBasedServer, func()) {
	tmpDir, err := os.MkdirTemp("", "abfs_server_test-*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v\n", err)
	}
	removeTmpDir := func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Logf("Failed to remove temp directory: %v\n", err)
		}
	}
	s, err := New(tmpDir)
	if err != nil {
		removeTmpDir()
		t.Fatalf("failed to create ABFS server with %q local directory: %v", tmpDir, err)
	}
	return s, func() {
		if err := s.Close(); err != nil {
			t.Logf("Failed to close the server: %v\n", err)
		}
		removeTmpDir()
	}
}

func TestPutObject_Headers(t *testing.T) {
	require := require.New(t)

	s, close := Setup(t)
	defer close()

	ctx := context.Background()
	content := []byte("blob 23\x00This is some\nTest Data\n")
	key := objects.HashBytes(content)

	headers, err := s.Headers(ctx, []Hash{key})
	require.NoError(err, "error reading Headers")

	require.Len(headers, 1)
	require.Equal(int64(0), headers[0].Size)
	require.True(headers[0].IsEmpty())

	err = s.PutObject(ctx, key, remote.NewZStdContentReader(content))
	require.NoError(err, "error from PutObject")

	headers, err = s.Headers(ctx, []Hash{key})
	require.NoError(err, "error reading Headers")

	require.Len(headers, 1)
	require.True(headers[0].IsBlob())
	require.Equal(int64(23), headers[0].Size)
}

func TestPutObject_GetObject(t *testing.T) {
	require := require.New(t)

	s, close := Setup(t)
	defer close()

	ctx := context.Background()
	content := []byte("blob 23\x00This is some\nTest Data\n")
	key := objects.HashBytes(content)

	_, err := s.GetObject(ctx, key)
	require.True(entities.IsNotExist(err))

	err = s.PutObject(ctx, key, remote.NewZStdContentReader(content))
	require.NoError(err, "error from PutObject")

	reader, err := s.GetObject(ctx, key)
	require.NoError(err, "error GetObject")

	reader, err = remote.DecodeToRaw(reader)
	require.NoError(err, "error decoding content")

	got, err := io.ReadAll(reader)
	require.NoError(err, "error reading content")
	require.Equal(got, content)
}

func TestPutObject_Duplicate(t *testing.T) {
	require := require.New(t)

	s, close := Setup(t)
	defer close()

	ctx := context.Background()
	content := []byte("blob 23\x00This is some\nTest Data\n")
	key := objects.HashBytes(content)

	err := s.PutObject(ctx, key, remote.NewZStdContentReader(content))
	require.NoError(err, "error from PutObject")

	err = s.PutObject(ctx, key, remote.NewZStdContentReader(content))
	require.NoError(err, "error from PutObject")
}

func TestPutObject_DuplicateBig(t *testing.T) {
	require := require.New(t)

	s, close := Setup(t)
	defer close()

	ctx := context.Background()
	content := fmt.Appendf(nil, "blob %d\x00%s", 13000000, strings.Repeat("hello world!\n", 1000000))
	key := objects.HashBytes(content)

	err := s.PutObject(ctx, key, remote.NewZStdContentReader(content))
	require.NoError(err, "error from PutObject")

	err = s.PutObject(ctx, key, remote.NewZStdContentReader(content))
	require.NoError(err, "error from PutObject")

	// Modified content should pass as well because it will be short-cuircuited
	err = s.PutObject(ctx, key, remote.NewZStdContentReader(append(content, 'X')))
	require.NoError(err, "error from PutObject")

	reader, err := s.GetObject(ctx, key)
	require.NoError(err, "error GetObject")

	reader, err = remote.DecodeToRaw(reader)
	require.NoError(err, "error decoding content")

	got, err := io.ReadAll(reader)
	require.NoError(err, "error reading content")
	require.Equal(got, content)
}
