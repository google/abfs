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

package depot

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"strings"
	"testing"

	"cloud.google.com/go/bigtable"
	"cloud.google.com/go/bigtable/bttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func setupTestBigtableDepot(t *testing.T, maxBytes int64) Depot {
	t.Helper()
	srv, err := bttest.NewServer("localhost:0")
	require.NoError(t, err)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	adminClient, err := bigtable.NewAdminClient(ctx, "proj", "inst", option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { adminClient.Close() })

	err = adminClient.CreateTable(ctx, "chunks")
	require.NoError(t, err)
	err = adminClient.CreateColumnFamily(ctx, "chunks", "data")
	require.NoError(t, err)

	client, err := bigtable.NewClient(ctx, "proj", "inst", option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	return NewBigtableDepot("bigtable", maxBytes, client, "chunks")
}

func TestBigtableDepotUniqueChunkIDsAndSafeRemove(t *testing.T) {
	ctx := context.Background()
	d := setupTestBigtableDepot(t, 1024*1024)
	master := NewMasterDepot([]Depot{d})

	payload := "identical payload content for testing unique chunk IDs"
	objHash := fmt.Sprintf("%x", sha1.Sum([]byte(payload)))

	namePrefix1 := objHash + "-uuid-1"
	chunks1, err := master.StoreChunks(ctx, namePrefix1, strings.NewReader(payload))
	require.NoError(t, err)
	require.Len(t, chunks1, 1)
	assert.Equal(t, namePrefix1+"-0", chunks1[0].ID)

	namePrefix2 := objHash + "-uuid-2"
	chunks2, err := master.StoreChunks(ctx, namePrefix2, strings.NewReader(payload))
	require.NoError(t, err)
	require.Len(t, chunks2, 1)
	assert.Equal(t, namePrefix2+"-0", chunks2[0].ID)

	// Storing identical content must generate distinct chunk IDs.
	assert.NotEqual(t, chunks1[0].ID, chunks2[0].ID)

	// Removing chunk1 must not affect chunk2.
	require.NoError(t, d.Remove(ctx, chunks1[0]))

	_, err = d.Load(ctx, chunks1[0])
	assert.ErrorIs(t, err, ErrNotFound)

	r, err := d.Load(ctx, chunks2[0])
	require.NoError(t, err)
	defer r.Close()
	loaded, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, payload, string(loaded))
}
