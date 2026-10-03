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

package localcache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/shared/entities"
)

func allFilesInDir(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return files, nil
}

func TestXxx(t *testing.T) {
	dir, err := os.MkdirTemp(os.TempDir(), "refStore-test-*")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, os.RemoveAll(dir))
	}()

	store, err := NewLocalStore(LocalStoreOpts{Dir: dir, Levels: 2, Notify: Notify{}, CompressMode: Uncompressed})
	require.NoError(t, err)

	err = store.Write(context.Background(), entities.EmptyDirHash, strings.NewReader("hello world"), nil)
	require.NoError(t, err)

	// Second write should loose
	err = store.Write(context.Background(), entities.EmptyDirHash, strings.NewReader("hello world2"), nil)
	require.NoError(t, err)

	content, err := store.Get(context.Background(), entities.EmptyDirHash)
	require.NoError(t, err)
	require.Equal(t, "hello world", string(content))

	err = store.Write(context.Background(), entities.EmptyFileHash, iotest.ErrReader(errors.New("bad read")), nil)
	require.Error(t, err)

	files, err := allFilesInDir(dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
}
