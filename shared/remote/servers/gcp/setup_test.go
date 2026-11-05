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
	"fmt"
	"testing"

	"cloud.google.com/go/bigtable"
	"cloud.google.com/go/spanner"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/remote"
)

func setupOpts(t *testing.T, dbID string) *SetupOpts {
	dropDatabaseAtCleanup(t, dbID)
	return &SetupOpts{
		ProjectID:  emulatorProject,
		InstanceID: emulatorInstance,
		DatabaseID: dbID,
		BtInstance: emulatorBigtable,
		BucketName: newEmulatorBucket(t),
	}
}

func TestSetup_CreatesMissingResourcesAndCanRunAgain(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	newEmulatorDatabase(t) // creates the emulator instance
	newEmptyEmulatorBigtable(t)
	opts := setupOpts(t, randomID(t, "t"))

	require.NoError(Setup(ctx, opts))
	require.NoError(Setup(ctx, opts))

	adm, err := bigtable.NewAdminClient(ctx, emulatorProject, emulatorBigtable)
	require.NoError(err)
	defer adm.Close()
	for _, table := range bigtableTables {
		info, err := adm.TableInfo(ctx, table.name)
		require.NoError(err)
		require.ElementsMatch(table.families, info.Families, table.name)
	}

	s, err := New(ctx, &NewServerOpts{
		ProjectID: opts.ProjectID, InstanceID: opts.InstanceID, DatabaseID: opts.DatabaseID,
		BtInstance: opts.BtInstance, BucketName: opts.BucketName, MaxCachedHeaders: 1024, MaxCachedContents: 1024,
	})
	require.NoError(err)
	defer s.Close()
	require.NoError(s.PutRefs(ctx, "example.com", "p", []remote.RefHash{{Ref: "refs/heads/main", Hash: entities.EmptyDirHash}}))
}

// The schema shipped before had a foreign key from Refs.Target to an Objects
// table the server never writes.
var schemaWithRefsTargetForeignKey = []string{
	"CREATE TABLE Objects (ObjectId STRING(128), AddTime TIMESTAMP NOT NULL, Type STRING(64) NOT NULL, Size INT64 NOT NULL, ContentEncoding STRING(64) NOT NULL, Content STRING(MAX) NOT NULL) PRIMARY KEY (ObjectId)",
	`CREATE SEQUENCE ProjectIdSequence OPTIONS (sequence_kind="bit_reversed_positive")`,
	"CREATE TABLE Projects (ProjectId INT64 DEFAULT (GET_NEXT_SEQUENCE_VALUE(SEQUENCE ProjectIdSequence)), Host STRING(MAX) NOT NULL, Project STRING(MAX) NOT NULL) PRIMARY KEY (ProjectId)",
	"CREATE UNIQUE INDEX ProjectsIndex ON Projects(Host, Project)",
	"CREATE TABLE Refs (ProjectId INT64 NOT NULL, Ref STRING(MAX) NOT NULL, UpdateTime TIMESTAMP NOT NULL, Target STRING(128), CONSTRAINT FK_Refs_ProjectId FOREIGN KEY (ProjectId) REFERENCES Projects (ProjectId), CONSTRAINT FK_Refs_TargetId FOREIGN KEY (Target) REFERENCES Objects (ObjectId)) PRIMARY KEY (ProjectId, Ref)",
	"CREATE INDEX RefsRefIndex ON Refs(Ref)",
}

func TestSetup_DropsRefsTargetForeignKeyAndKeepsData(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	newEmulatorDatabase(t)
	newEmulatorBigtable(t)
	opts := setupOpts(t, randomID(t, "t"))
	spannerDB := fmt.Sprintf("projects/%s/instances/%s/databases/%s", opts.ProjectID, opts.InstanceID, opts.DatabaseID)

	dc, err := database.NewDatabaseAdminClient(ctx)
	require.NoError(err)
	defer dc.Close()
	op, err := dc.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          fmt.Sprintf("projects/%s/instances/%s", opts.ProjectID, opts.InstanceID),
		CreateStatement: "CREATE DATABASE `" + opts.DatabaseID + "`",
		ExtraStatements: schemaWithRefsTargetForeignKey,
	})
	require.NoError(err)
	_, err = op.Wait(ctx)
	require.NoError(err)
	client, err := spanner.NewClient(ctx, spannerDB)
	require.NoError(err)
	defer client.Close()
	_, err = client.Apply(ctx, []*spanner.Mutation{spanner.Insert("Projects", []string{"Host", "Project"}, []any{"example.com", "kept"})})
	require.NoError(err)

	require.NoError(Setup(ctx, opts))

	s, err := New(ctx, &NewServerOpts{
		ProjectID: opts.ProjectID, InstanceID: opts.InstanceID, DatabaseID: opts.DatabaseID,
		BtInstance: opts.BtInstance, BucketName: opts.BucketName, MaxCachedHeaders: 1024, MaxCachedContents: 1024,
	})
	require.NoError(err)
	defer s.Close()
	require.NoError(s.PutRefs(ctx, "example.com", "kept", []remote.RefHash{{Ref: "refs/heads/main", Hash: entities.EmptyDirHash}}))
	refs, err := s.SearchRefs(ctx, "example.com", "", "", 0)
	require.NoError(err)
	require.Equal([]remote.ProjectRefHash{{Project: "kept", Ref: "refs/heads/main", Hash: entities.EmptyDirHash}}, refs)
}

func TestSetup_MissingBucket(t *testing.T) {
	newEmulatorDatabase(t)
	newEmulatorBigtable(t)
	opts := setupOpts(t, randomID(t, "t"))
	opts.BucketName = "no-such-bucket"
	require.ErrorContains(t, Setup(context.Background(), opts), "no-such-bucket")
}

func TestNew_ChecksResources(t *testing.T) {
	ctx := context.Background()
	newOpts := func(o *SetupOpts) *NewServerOpts {
		return &NewServerOpts{
			ProjectID: o.ProjectID, InstanceID: o.InstanceID, DatabaseID: o.DatabaseID,
			BtInstance: o.BtInstance, BucketName: o.BucketName, MaxCachedHeaders: 1024, MaxCachedContents: 1024,
		}
	}
	tests := []struct {
		name    string
		prepare func(t *testing.T) *SetupOpts
		wantErr string
	}{
		{
			name: "missing bucket",
			prepare: func(t *testing.T) *SetupOpts {
				newEmulatorBigtable(t)
				o := setupOpts(t, newEmulatorDatabase(t))
				o.BucketName = "no-such-bucket"
				return o
			},
			wantErr: "no-such-bucket",
		},
		{
			name: "missing Bigtable table",
			prepare: func(t *testing.T) *SetupOpts {
				newEmptyEmulatorBigtable(t)
				return setupOpts(t, newEmulatorDatabase(t))
			},
			wantErr: "Bigtable table objects",
		},
		{
			name: "missing Spanner table",
			prepare: func(t *testing.T) *SetupOpts {
				newEmulatorBigtable(t)
				newEmulatorDatabase(t)
				o := setupOpts(t, randomID(t, "t"))
				createDatabase(t, o, []string{"CREATE TABLE Other (Id INT64) PRIMARY KEY (Id)"})
				return o
			},
			wantErr: "Projects",
		},
		{
			name: "refs target foreign key",
			prepare: func(t *testing.T) *SetupOpts {
				newEmulatorBigtable(t)
				newEmulatorDatabase(t)
				o := setupOpts(t, randomID(t, "t"))
				createDatabase(t, o, schemaWithRefsTargetForeignKey)
				return o
			},
			wantErr: "FK_Refs_TargetId",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := tt.prepare(t)
			s, err := New(ctx, newOpts(o))
			if err == nil {
				_ = s.Close()
			}
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorContains(t, err, "abfs server gcp init")
		})
	}
}

func createDatabase(t *testing.T, o *SetupOpts, ddl []string) {
	t.Helper()
	ctx := context.Background()
	dc, err := database.NewDatabaseAdminClient(ctx)
	require.NoError(t, err)
	defer dc.Close()
	op, err := dc.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          fmt.Sprintf("projects/%s/instances/%s", o.ProjectID, o.InstanceID),
		CreateStatement: "CREATE DATABASE `" + o.DatabaseID + "`",
		ExtraStatements: ddl,
	})
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)
}
