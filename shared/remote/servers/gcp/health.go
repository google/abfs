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
)

// healthProbeTables are the Bigtable tables of objects, maps and chunks, which
// the health probe reads. The list is fixed rather than taken from
// bigtableTables, so that the probe reads only the tables every server has.
var healthProbeTables = []string{"objects", "maps", "chunks"}

// HealthProbes returns the backend health checks of the server by backend
// name. Each one reads from its backend and never writes, so a lost write
// permission is not detected.
func (s *GCPServer) HealthProbes() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"spanner":  s.casdb.CheckTables,
		"bigtable": s.checkBigtable,
		"storage": func(ctx context.Context) error {
			return checkBucketWith(ctx, s.storage, s.bucketName)
		},
	}
}

// checkBigtable reads an absent row of each table the health probe covers.
func (s *GCPServer) checkBigtable(ctx context.Context) error {
	for _, table := range healthProbeTables {
		if _, err := s.casdb.BTClient().Open(table).ReadRow(ctx, "abfs-setup-check"); err != nil {
			return fmt.Errorf("cannot read Bigtable table %s: %w", table, err)
		}
	}
	return nil
}
