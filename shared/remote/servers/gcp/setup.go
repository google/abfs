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
	"errors"
	"fmt"
	"log"
	"slices"

	"cloud.google.com/go/bigtable"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/google/abfs/shared/remote/servers/gcp/casdb"
)

// bigtableTables lists the Bigtable tables of the server and their column
// families.
var bigtableTables = []struct {
	name     string
	families []string
}{
	{"objects", []string{"meta", "data"}},
	{"maps", []string{"cf"}},
	{"chunks", []string{"data"}},
}

type SetupOpts struct {
	ProjectID  string
	InstanceID string
	DatabaseID string
	BtInstance string
	BucketName string
}

// Setup prepares existing Spanner and Bigtable instances and a bucket for the
// server: it creates the database, the schema, the tables and the column
// families that are missing, and checks that the bucket exists. It never deletes
// data, so it can run again after an upgrade.
func Setup(ctx context.Context, opts *SetupOpts) error {
	spannerDB := fmt.Sprintf("projects/%s/instances/%s/databases/%s", opts.ProjectID, opts.InstanceID, opts.DatabaseID)
	if err := casdb.SetupSchema(ctx, spannerDB); err != nil {
		return err
	}
	if err := setupBigtable(ctx, opts.ProjectID, opts.BtInstance); err != nil {
		return err
	}
	return checkBucket(ctx, opts.BucketName)
}

func setupBigtable(ctx context.Context, project, instance string) error {
	admin, err := bigtable.NewAdminClient(ctx, project, instance)
	if err != nil {
		return fmt.Errorf("failed to create the Bigtable admin client: %w", err)
	}
	defer admin.Close()

	existing, err := admin.Tables(ctx)
	if err != nil {
		return fmt.Errorf("failed to list the tables of Bigtable instance %s: %w", instance, err)
	}
	for _, table := range bigtableTables {
		if !slices.Contains(existing, table.name) {
			if err := admin.CreateTable(ctx, table.name); err != nil {
				return fmt.Errorf("failed to create Bigtable table %s: %w", table.name, err)
			}
			log.Printf("Created Bigtable table %s", table.name)
		}
		info, err := admin.TableInfo(ctx, table.name)
		if err != nil {
			return fmt.Errorf("failed to read Bigtable table %s: %w", table.name, err)
		}
		for _, family := range table.families {
			if slices.ContainsFunc(info.FamilyInfos, func(f bigtable.FamilyInfo) bool { return f.Name == family }) {
				continue
			}
			if err := admin.CreateColumnFamily(ctx, table.name, family); err != nil {
				return fmt.Errorf("failed to create column family %s of Bigtable table %s: %w", family, table.name, err)
			}
			// The server reads only the newest cell of each column.
			if err := admin.SetGCPolicy(ctx, table.name, family, bigtable.MaxVersionsPolicy(1)); err != nil {
				return fmt.Errorf("failed to set the GC policy of %s:%s: %w", table.name, family, err)
			}
			log.Printf("Created column family %s:%s", table.name, family)
		}
	}
	return nil
}

// checkBucket checks that the bucket exists and its objects can be listed.
func checkBucket(ctx context.Context, bucketName string) error {
	client, err := storage.NewGRPCClient(ctx, storage.WithDisabledClientMetrics())
	if err != nil {
		return fmt.Errorf("failed to create the Cloud Storage client: %w", err)
	}
	defer client.Close()
	_, err = client.Bucket(bucketName).Objects(ctx, &storage.Query{Prefix: "abfs-setup-check/"}).Next()
	if err != nil && !errors.Is(err, iterator.Done) {
		return fmt.Errorf("cannot list objects of bucket %s: %w", bucketName, err)
	}
	return nil
}
