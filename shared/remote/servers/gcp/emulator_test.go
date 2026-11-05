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

package gcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"cloud.google.com/go/bigtable"
	"cloud.google.com/go/bigtable/bttest"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/abfs/shared/remote/servers/gcp/casdb"
	"github.com/google/abfs/shared/remote/servers/gcp/depot"
)

const (
	emulatorProject  = "test-proj"
	emulatorInstance = "test-inst"
	emulatorBigtable = "test-bt"
)

// The tests in this file need the Spanner emulator (SPANNER_EMULATOR_HOST),
// because the fake Spanner server answers only a few statements and does not
// enforce the schema. Bigtable runs in process.

func requireSpannerEmulator(t *testing.T) {
	t.Helper()
	if os.Getenv("SPANNER_EMULATOR_HOST") == "" {
		t.Skip("SPANNER_EMULATOR_HOST is not set")
	}
}

func randomID(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 6)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return prefix + hex.EncodeToString(b)
}

// newEmulatorDatabase creates a database with the server schema on the Spanner
// emulator and returns its ID.
func newEmulatorDatabase(t *testing.T) string {
	t.Helper()
	requireSpannerEmulator(t)
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

	dbID := randomID(t, "t")
	dc, err := database.NewDatabaseAdminClient(ctx)
	require.NoError(t, err)
	defer dc.Close()
	dop, err := dc.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          fmt.Sprintf("projects/%s/instances/%s", emulatorProject, emulatorInstance),
		CreateStatement: "CREATE DATABASE `" + dbID + "`",
		ExtraStatements: casdb.SchemaStatements(),
	})
	require.NoError(t, err)
	_, err = dop.Wait(ctx)
	require.NoError(t, err)
	return dbID
}

// newEmulatorBigtable starts an in-process Bigtable with the server tables.
func newEmulatorBigtable(t *testing.T) {
	t.Helper()
	newEmptyEmulatorBigtable(t)
	adm, err := bigtable.NewAdminClient(context.Background(), emulatorProject, emulatorBigtable)
	require.NoError(t, err)
	defer adm.Close()
	for _, table := range bigtableTables {
		require.NoError(t, adm.CreateTable(context.Background(), table.name))
		for _, f := range table.families {
			require.NoError(t, adm.CreateColumnFamily(context.Background(), table.name, f))
		}
	}
}

// newEmptyEmulatorBigtable starts an in-process Bigtable without tables.
func newEmptyEmulatorBigtable(t *testing.T) {
	t.Helper()
	srv, err := bttest.NewServer("localhost:0")
	require.NoError(t, err)
	t.Cleanup(srv.Close)
	t.Setenv("BIGTABLE_EMULATOR_HOST", srv.Addr)
}

// newEmulatorCASDBServer returns a server whose metadata is on the emulators and
// whose content is in memory.
func newEmulatorCASDBServer(t *testing.T) *GCPServer {
	t.Helper()
	dbID := newEmulatorDatabase(t)
	newEmulatorBigtable(t)

	db, err := casdb.New(context.Background(), fmt.Sprintf("projects/%s/instances/%s/databases/%s", emulatorProject, emulatorInstance, dbID), emulatorProject, emulatorBigtable, "", 1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	s := createTestServer(t, depot.NewMasterDepot([]depot.Depot{depot.NewMemoryDepot("mem", 1000000)}))
	s.casdb = db
	s.addObject = db.AddObject
	s.projectsByName = make(map[[2]string]int64)
	s.projectsByID = make(map[int64][2]string)
	return s
}

// newEmulatorBucket creates a bucket on the Cloud Storage emulator named by
// STORAGE_EMULATOR_HOST_GRPC and returns its name.
func newEmulatorBucket(t *testing.T) string {
	t.Helper()
	if os.Getenv("STORAGE_EMULATOR_HOST_GRPC") == "" {
		t.Skip("STORAGE_EMULATOR_HOST_GRPC is not set")
	}
	ctx := context.Background()
	client, err := storage.NewGRPCClient(ctx)
	require.NoError(t, err)
	defer client.Close()
	name := randomID(t, "b")
	require.NoError(t, client.Bucket(name).Create(ctx, emulatorProject, nil))
	return name
}

// newEmulatorServer returns a server created by New on the emulators.
func newEmulatorServer(t *testing.T, opts *NewServerOpts) *GCPServer {
	t.Helper()
	requireSpannerEmulator(t)
	bucket := newEmulatorBucket(t)
	dbID := newEmulatorDatabase(t)
	newEmulatorBigtable(t)

	o := NewServerOpts{}
	if opts != nil {
		o = *opts
	}
	o.ProjectID = emulatorProject
	o.InstanceID = emulatorInstance
	o.DatabaseID = dbID
	o.BtInstance = emulatorBigtable
	o.BucketName = bucket
	if o.MaxCachedHeaders == 0 {
		o.MaxCachedHeaders = 1024
	}
	if o.MaxCachedContents == 0 {
		o.MaxCachedContents = 1024
	}
	s, err := New(context.Background(), &o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}
