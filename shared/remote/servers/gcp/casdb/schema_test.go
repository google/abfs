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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"cloud.google.com/go/bigtable/bttest"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
)

const (
	emulatorProject  = "test-proj"
	emulatorInstance = "test-inst"
)

// newEmulatorDatabase creates a database with the given DDL on the Spanner emulator
// named by SPANNER_EMULATOR_HOST and returns its path. The test is skipped when the
// variable is not set, because only the emulator enforces the schema.
func newEmulatorDatabase(t *testing.T, ddl []string) string {
	t.Helper()
	if os.Getenv("SPANNER_EMULATOR_HOST") == "" {
		t.Skip("SPANNER_EMULATOR_HOST is not set")
	}
	ctx := context.Background()

	ic, err := instance.NewInstanceAdminClient(ctx)
	require.NoError(t, err)
	defer ic.Close()
	op, err := ic.CreateInstance(ctx, &instancepb.CreateInstanceRequest{
		Parent:     "projects/" + emulatorProject,
		InstanceId: emulatorInstance,
		Instance: &instancepb.Instance{
			Config:      fmt.Sprintf("projects/%s/instanceConfigs/emulator-config", emulatorProject),
			DisplayName: emulatorInstance,
			NodeCount:   1,
		},
	})
	if status.Code(err) != codes.AlreadyExists {
		require.NoError(t, err)
		_, err = op.Wait(ctx)
		require.NoError(t, err)
	}

	b := make([]byte, 6)
	_, err = rand.Read(b)
	require.NoError(t, err)
	dbID := "t" + hex.EncodeToString(b)

	dc, err := database.NewDatabaseAdminClient(ctx)
	require.NoError(t, err)
	defer dc.Close()
	dop, err := dc.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          fmt.Sprintf("projects/%s/instances/%s", emulatorProject, emulatorInstance),
		CreateStatement: "CREATE DATABASE `" + dbID + "`",
		ExtraStatements: ddl,
	})
	require.NoError(t, err)
	_, err = dop.Wait(ctx)
	require.NoError(t, err)
	dbPath := fmt.Sprintf("projects/%s/instances/%s/databases/%s", emulatorProject, emulatorInstance, dbID)
	// The emulator limits the number of databases per instance.
	t.Cleanup(func() {
		dc, err := database.NewDatabaseAdminClient(context.Background())
		if err != nil {
			return
		}
		defer dc.Close()
		_ = dc.DropDatabase(context.Background(), &databasepb.DropDatabaseRequest{Database: dbPath})
	})
	return dbPath
}

func newEmulatorCASDB(t *testing.T) (*CASDB, string) {
	t.Helper()
	dbPath := newEmulatorDatabase(t, SchemaStatements())
	bt, err := bttest.NewServer("localhost:0")
	require.NoError(t, err)
	t.Cleanup(bt.Close)
	t.Setenv("BIGTABLE_EMULATOR_HOST", bt.Addr)

	c, err := New(context.Background(), dbPath, emulatorProject, "test-bt", "", 1, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c, dbPath
}

func TestSchema_RefsAcceptAnyExistingTarget(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	c, _ := newEmulatorCASDB(t)

	id, err := c.GetOrCreateProject(ctx, "example.com", "demo/project")
	require.NoError(err)

	// Objects live in Bigtable, so Spanner has no row a ref target could point to.
	h1 := entities.NewHash("9cc07fd385314fecaca339bcb1964b5481dce5f2")
	h2 := entities.NewHash("366f88c58142fe0c9225f7fa08649bac5f73e546")
	require.NoError(c.AddRefs(ctx, id, []remote.RefHash{{Ref: "refs/heads/main", Hash: h1}}))
	require.NoError(c.XchgRefs(ctx, id, []remote.RefXchg{{Ref: "refs/heads/main", Current: h1, Desired: h2}}))

	got, err := c.GetRefs(ctx, id, []string{"refs/heads/main"})
	require.NoError(err)
	require.Equal([]entities.Hash{h2}, got)
}

func TestSchema_StatementsCanBeReappliedWithoutDataLoss(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	c, dbPath := newEmulatorCASDB(t)

	id, err := c.GetOrCreateProject(ctx, "example.com", "demo/project")
	require.NoError(err)

	dc, err := database.NewDatabaseAdminClient(ctx)
	require.NoError(err)
	defer dc.Close()
	op, err := dc.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
		Database:   dbPath,
		Statements: SchemaStatements(),
	})
	require.NoError(err)
	require.NoError(op.Wait(ctx))

	got, err := c.GetProjectID(ctx, "example.com", "demo/project")
	require.NoError(err)
	require.Equal(id, got)
}

func TestCASDB_CheckTables(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	c, dbPath := newEmulatorCASDB(t)
	require.NoError(c.CheckTables(ctx))
	require.NoError(c.CheckSchema(ctx))

	dc, err := database.NewDatabaseAdminClient(ctx)
	require.NoError(err)
	defer dc.Close()
	op, err := dc.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
		Database:   dbPath,
		Statements: []string{"DROP INDEX RefsRefIndex", "DROP TABLE Refs"},
	})
	require.NoError(err)
	require.NoError(op.Wait(ctx))

	require.ErrorContains(c.CheckTables(ctx), "Spanner table Refs")
	require.ErrorContains(c.CheckSchema(ctx), "Spanner table Refs")
}
