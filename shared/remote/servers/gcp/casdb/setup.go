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
	"fmt"
	"log"
	"strings"

	database "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// refsTargetForeignKey is the constraint earlier schemas put on Refs.Target. It
// referenced a Spanner Objects table the server never writes, so every ref with
// a target failed.
const refsTargetForeignKey = "FK_Refs_TargetId"

// SetupSchema creates the Spanner database if it is missing and applies the
// schema to it. It drops the ref target foreign key of earlier schemas and
// leaves every other table and its data in place.
func SetupSchema(ctx context.Context, spannerDB string) error {
	client, err := database.NewDatabaseAdminClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create the Spanner database admin client: %w", err)
	}
	defer client.Close()

	ddl, err := client.GetDatabaseDdl(ctx, &databasepb.GetDatabaseDdlRequest{Database: spannerDB})
	if status.Code(err) == codes.NotFound {
		return createDatabase(ctx, client, spannerDB)
	}
	if err != nil {
		return fmt.Errorf("failed to read the schema of %s: %w", spannerDB, err)
	}

	var stmts []string
	for _, s := range ddl.GetStatements() {
		if strings.Contains(s, "CONSTRAINT "+refsTargetForeignKey+" ") {
			stmts = append(stmts, "ALTER TABLE Refs DROP CONSTRAINT "+refsTargetForeignKey)
			break
		}
	}
	stmts = append(stmts, SchemaStatements()...)
	op, err := client.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{Database: spannerDB, Statements: stmts})
	if err != nil {
		return fmt.Errorf("failed to update the schema of %s: %w", spannerDB, err)
	}
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("failed to update the schema of %s: %w", spannerDB, err)
	}
	log.Printf("Applied %d schema statements to %s", len(stmts), spannerDB)
	return nil
}

func createDatabase(ctx context.Context, client *database.DatabaseAdminClient, spannerDB string) error {
	i := strings.LastIndex(spannerDB, "/databases/")
	if i < 0 {
		return fmt.Errorf("invalid Spanner database path %q", spannerDB)
	}
	op, err := client.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          spannerDB[:i],
		CreateStatement: "CREATE DATABASE `" + spannerDB[i+len("/databases/"):] + "`",
		ExtraStatements: SchemaStatements(),
	})
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", spannerDB, err)
	}
	if _, err := op.Wait(ctx); err != nil {
		return fmt.Errorf("failed to create %s: %w", spannerDB, err)
	}
	log.Printf("Created %s", spannerDB)
	return nil
}
