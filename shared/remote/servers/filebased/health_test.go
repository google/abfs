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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHealthProbes_Store(t *testing.T) {
	tests := []struct {
		name    string
		root    bool // the case does not work for root
		prepare func(t *testing.T, dir string)
		wantErr string
	}{
		{
			name: "new store",
		},
		{
			name: "leftover probe file",
			prepare: func(t *testing.T, dir string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, healthProbeFile), []byte("x"), 0o600))
			},
		},
		{
			name: "store directory moved away",
			prepare: func(t *testing.T, dir string) {
				require.NoError(t, os.Rename(dir, dir+".gone"))
			},
			wantErr: "failed to stat the store directory",
		},
		{
			name: "store directory replaced",
			prepare: func(t *testing.T, dir string) {
				require.NoError(t, os.Rename(dir, dir+".gone"))
				require.NoError(t, os.Mkdir(dir, 0o755))
			},
			wantErr: "was replaced",
		},
		{
			name: "store directory not accessible",
			root: true,
			prepare: func(t *testing.T, dir string) {
				require.NoError(t, os.Chmod(dir, 0))
				t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			},
			wantErr: "permission denied",
		},
		{
			name: "empty file object missing",
			prepare: func(t *testing.T, dir string) {
				require.NoError(t, os.RemoveAll(filepath.Join(dir, "objects")))
			},
			wantErr: "failed to open the empty file object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.root && os.Geteuid() == 0 {
				t.Skip("root ignores file permissions")
			}
			require := require.New(t)
			dir := filepath.Join(t.TempDir(), "store")
			s, err := New(dir)
			require.NoError(err)
			t.Cleanup(func() { _ = s.Close() })
			if tt.prepare != nil {
				tt.prepare(t, dir)
			}

			probes := s.HealthProbes()
			require.Len(probes, 1)
			err = probes["store"](context.Background())
			if tt.wantErr != "" {
				require.ErrorContains(err, tt.wantErr)
				return
			}
			require.NoError(err)
			_, err = os.Stat(filepath.Join(dir, healthProbeFile))
			require.ErrorIs(err, os.ErrNotExist, "the probe file is left behind")
		})
	}
}

func TestHealthProbes_StoreRecovers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	require := require.New(t)
	dir := filepath.Join(t.TempDir(), "store")
	s, err := New(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = s.Close() })
	check := s.HealthProbes()["store"]

	require.NoError(os.Chmod(dir, 0))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	require.Error(check(context.Background()))
	require.NoError(os.Chmod(dir, 0o755))
	require.NoError(check(context.Background()))
	entries, err := os.ReadDir(dir)
	require.NoError(err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.ElementsMatch([]string{"objects", "map", "refs"}, names)
}
