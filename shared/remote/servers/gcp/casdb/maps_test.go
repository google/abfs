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

package casdb

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/bigtable"
	"cloud.google.com/go/bigtable/bttest"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/google/abfs/shared/entities"
)

func newTestCASDBWithBigtable(t *testing.T) *CASDB {
	t.Helper()
	ctx := context.Background()
	srv, err := bttest.NewServer("localhost:0")
	require.NoError(t, err)
	t.Cleanup(srv.Close)
	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	adm, err := bigtable.NewAdminClient(ctx, "test-proj", "test-bt", option.WithGRPCConn(conn))
	require.NoError(t, err)
	require.NoError(t, adm.CreateTable(ctx, "maps"))
	require.NoError(t, adm.CreateColumnFamily(ctx, "maps", "cf"))

	client, err := bigtable.NewClient(ctx, "test-proj", "test-bt", option.WithGRPCConn(conn))
	require.NoError(t, err)
	return &CASDB{btClient: client, mapsTable: client.Open("maps")}
}

func TestMapResults_InsertionOrder(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	c := newTestCASDBWithBigtable(t)

	from := entities.NewHash("1234123412341234123412341234123412341234")
	// Added in an order that is neither sorted nor reverse sorted.
	tos := []entities.Hash{
		entities.NewHash("cccccccccccccccccccccccccccccccccccccccc"),
		entities.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		entities.NewHash("ffffffffffffffffffffffffffffffffffffffff"),
		entities.NewHash("5555555555555555555555555555555555555555"),
	}
	add := func(to entities.Hash) {
		require.NoError(c.AddMapResult(ctx, from, to))
		// Cell timestamps have millisecond granularity.
		time.Sleep(2 * time.Millisecond)
	}
	for _, to := range tos {
		add(to)
	}
	got, err := c.MapResults(ctx, from)
	require.NoError(err)
	require.Equal(tos, got)

	// Adding a result again makes it the newest, as appending does in file-based.
	add(tos[0])
	got, err = c.MapResults(ctx, from)
	require.NoError(err)
	require.Equal([]entities.Hash{tos[1], tos[2], tos[3], tos[0]}, got)
}
