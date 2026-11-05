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

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func TestReadBuildStart_BelowTheMountRoot(t *testing.T) {
	// abfs-wrap runs in the Soong top; .ninjago.conf there says Chroot: android.
	top := filepath.Join(t.TempDir(), "android")
	const hash = "0123456789abcdef0123456789abcdef01234567"
	require.NoError(t, os.MkdirAll(filepath.Join(top, entities.OutDir), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(top, entities.OutDir, ".build_start"), []byte(`{"BuildID": "b1", "Hash": "`+hash+`"}`), 0644))
	t.Chdir(top)

	bs, err := readBuildStart()
	require.NoError(t, err)
	require.Equal(t, "b1", bs.BuildID)
	require.Equal(t, hash, bs.Hash.String())
}
