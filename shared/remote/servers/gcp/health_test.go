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
	"maps"
	"slices"
	"testing"

	"cloud.google.com/go/bigtable"
	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/remote/servers/gcp/depot"
	"github.com/google/abfs/shared/remote/servers/inmemory"
)

type healthProber interface {
	HealthProbes() map[string]func(context.Context) error
}

func TestFilterServer_HealthProbes(t *testing.T) {
	s := createTestServer(t, depot.NewMasterDepot([]depot.Depot{depot.NewMemoryDepot("mem", 1000)}))
	var f any = NewFilterServer(s, nil)
	probes := f.(healthProber).HealthProbes()
	require.ElementsMatch(t, []string{"spanner", "bigtable", "storage"}, slices.Collect(maps.Keys(probes)))

	f = NewFilterServer(inmemory.New(), nil)
	require.Nil(t, f.(healthProber).HealthProbes())
}

func TestHealthProbes_Bigtable(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	s, _ := createTestServerWithCASDB(t)
	adm, err := bigtable.NewAdminClient(ctx, "test-proj", "test-bt")
	require.NoError(err)
	defer adm.Close()
	require.ErrorContains(s.checkBigtable(ctx), "Bigtable table maps")

	require.NoError(adm.CreateTable(ctx, "maps"))
	require.NoError(adm.CreateTable(ctx, "chunks"))
	require.NoError(s.HealthProbes()["bigtable"](ctx))

	require.NoError(adm.DeleteTable(ctx, "chunks"))
	require.ErrorContains(s.HealthProbes()["bigtable"](ctx), "Bigtable table chunks")
}

func TestHealthProbes_Emulators(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	s := newEmulatorServer(t, nil)
	probes := NewFilterServer(s, nil).HealthProbes()
	for name, probe := range probes {
		require.NoError(probe(ctx), name)
	}

	adm, err := bigtable.NewAdminClient(ctx, emulatorProject, emulatorBigtable)
	require.NoError(err)
	defer adm.Close()
	require.NoError(adm.DeleteTable(ctx, "maps"))
	require.ErrorContains(probes["bigtable"](ctx), "Bigtable table maps")
	require.NoError(probes["spanner"](ctx))
	require.NoError(probes["storage"](ctx))

	s.bucketName = "no-such-bucket"
	require.ErrorContains(probes["storage"](ctx), "no-such-bucket")
}
