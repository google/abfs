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

package ninjago

import (
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDepsResolve(t *testing.T) {
	root, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, os.RemoveAll(root))
	}()

	file1 := path.Join(root, "file1")
	err = os.WriteFile(file1, []byte("hello"), 0644)
	require.NoError(t, err)

	f, syms, err := evalSymlinks(root, file1)
	require.NoError(t, err)
	require.Equal(t, "file1", f)
	require.Nil(t, syms)

	sym1 := path.Join(root, "sym1")
	err = os.Symlink("file1", sym1)
	require.NoError(t, err)
	f, syms, err = evalSymlinks(root, sym1)
	require.NoError(t, err)
	require.Equal(t, "file1", f)
	require.Equal(t, []string{"sym1"}, syms)
}
